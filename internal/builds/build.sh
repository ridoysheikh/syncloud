# SynCloud build (§5.8), run as a job in the BuildKit image: fetch one commit,
# skip it when no watched path changed, pick the builder (Dockerfile, then
# Nixpacks, then a static site), build with BuildKit and push.
# Everything comes from the environment; see buildSpec in builds.go.
set -eu
step() { printf '==> %s\n' "$*"; }

mkdir -p "$DOCKER_CONFIG"
printf '%s' "$BUILD_REGISTRY_AUTH" > "$DOCKER_CONFIG/config.json"
# Private networks: the registry and the built-in Git server have
# self-signed certificates, passed in to be trusted.
if [ -n "${REGISTRY_CA:-}" ]; then
  printf '%s\n' "$REGISTRY_CA" > /tmp/registry-ca.pem
  printf '[registry."%s"]\n  ca = ["/tmp/registry-ca.pem"]\n' "$REGISTRY_HOST" > /tmp/buildkitd.toml
  export BUILDKITD_FLAGS="${BUILDKITD_FLAGS:-} --config /tmp/buildkitd.toml"
fi
if [ -n "${GIT_CA:-}" ]; then
  printf '%s\n' "$GIT_CA" > /tmp/git-ca.pem
  git config --global http.sslCAInfo /tmp/git-ca.pem
fi

step "fetching $GIT_REF at $(printf %.12s "$GIT_SHA")"
git init -q /src
cd /src
git remote add origin "$GIT_URL"
if [ -n "${GIT_TOKEN:-}" ]; then
  git config http.extraHeader "Authorization: Basic $(printf 'syncloud:%s' "$GIT_TOKEN" | base64 | tr -d '\n')"
fi
fetch() { git -c protocol.version=2 fetch -q --depth=1 origin "$1" 2>/dev/null; }
if ! fetch "$GIT_SHA"; then
  # Servers that refuse fetching a commit by hash: fetch the ref instead.
  git fetch -q --depth=50 origin "$GIT_REF" || { echo "cannot fetch $GIT_REF"; exit 1; }
  git cat-file -e "$GIT_SHA^{commit}" 2>/dev/null || { echo "commit $GIT_SHA is no longer on $GIT_REF"; exit 1; }
fi
git -c advice.detachedHead=false checkout -q "$GIT_SHA"

# Path filters: build only when a changed file matches an include pattern
# and no "!" exclude pattern. "*" matches across directories.
if [ -n "${WATCH_PATHS:-}" ] && [ -n "${BASE_SHA:-}" ]; then
  set -f # patterns and file names are matched, never expanded
  if fetch "$BASE_SHA" && changed=$(git diff --name-only "$BASE_SHA" "$GIT_SHA"); then
    has_inc=""
    for p in $WATCH_PATHS; do case "$p" in !*) ;; *) has_inc=1 ;; esac; done
    hit=""
    for f in $changed; do
      inc=""
      [ -n "$has_inc" ] || inc=1
      exc=""
      for p in $WATCH_PATHS; do
        case "$p" in
          !*) case "$f" in ${p#!}) exc=1 ;; esac ;;
          *) case "$f" in $p) inc=1 ;; esac ;;
        esac
      done
      if [ -n "$inc" ] && [ -z "$exc" ]; then hit=$f; break; fi
    done
    if [ -z "$hit" ]; then
      step "skipped: no changed file matches the watch paths ($(echo $WATCH_PATHS))"
      printf '%s\n' "$changed" | head -n 20 | sed 's/^/    /'
      exit 78
    fi
    step "$hit matches the watch paths"
  else
    step "cannot compare with $(printf %.12s "$BASE_SHA"): building anyway"
  fi
  set +f
fi

cd "/src/${CONTEXT_DIR:-.}" 2>/dev/null || { echo "context directory ${CONTEXT_DIR} not found"; exit 1; }
kind=$BUILDER
if [ "$kind" = auto ]; then
  if [ -f "$DOCKERFILE" ]; then kind=dockerfile; else kind=nixpacks; fi
fi
dfdir=.
dfname=$DOCKERFILE
case "$kind" in
  dockerfile)
    [ -f "$DOCKERFILE" ] || { echo "no $DOCKERFILE in ${CONTEXT_DIR:-the repository root}"; exit 1; }
    dfdir=$(dirname "$DOCKERFILE")
    dfname=$(basename "$DOCKERFILE")
    ;;
  nixpacks)
    case $(uname -m) in
      x86_64) arch=x86_64 sum=$NIXPACKS_SHA256_X86_64 ;;
      aarch64) arch=aarch64 sum=$NIXPACKS_SHA256_AARCH64 ;;
      *) echo "Nixpacks does not support $(uname -m)"; exit 1 ;;
    esac
    step "no $DOCKERFILE: detecting the app with Nixpacks $NIXPACKS_VERSION"
    wget -qO /tmp/nixpacks.tgz "https://github.com/railwayapp/nixpacks/releases/download/v$NIXPACKS_VERSION/nixpacks-v$NIXPACKS_VERSION-$arch-unknown-linux-musl.tar.gz" ||
      { echo "cannot download Nixpacks (does the build node have internet access?)"; exit 1; }
    echo "$sum  /tmp/nixpacks.tgz" | sha256sum -c -s || { echo "the Nixpacks download does not match its checksum"; exit 1; }
    tar -xzf /tmp/nixpacks.tgz -C /tmp nixpacks
    providers=$(/tmp/nixpacks detect . | tr '\n' ' ')
    if [ "$BUILDER" = auto ] && { [ -z "${providers% }" ] || [ "$providers" = "staticfile " ]; }; then
      kind=static
    else
      [ -n "${providers% }" ] || { echo "Nixpacks does not recognize this app: add a $DOCKERFILE"; exit 1; }
      step "Nixpacks: ${providers% }"
      # NIXPACKS_INSTALL_CMD, _BUILD_CMD and _START_CMD (the source's
      # overrides) are read from the environment; build variables are --env.
      set --
      while IFS= read -r kv; do [ -n "$kv" ] && set -- "$@" --env "$kv"; done <<EOF
${BUILD_VARS:-}
EOF
      /tmp/nixpacks build . --out . "$@"
      dfdir=.nixpacks
      dfname=Dockerfile
    fi
    ;;
esac
if [ "$kind" = static ]; then
  [ -f index.html ] || { echo "nothing to build: no $DOCKERFILE, no app Nixpacks recognizes and no index.html"; exit 1; }
  step "static site: serving the files with $STATIC_IMAGE on port 80"
  mkdir -p /tmp/static
  printf 'FROM %s\nCOPY . /usr/share/nginx/html\n' "$STATIC_IMAGE" > /tmp/static/Dockerfile
  dfdir=/tmp/static
  dfname=Dockerfile
fi

step "building with BuildKit ($kind)"
# Build variables (KEY=VALUE lines) are the Dockerfile's build arguments.
set --
while IFS= read -r kv; do [ -n "$kv" ] && set -- "$@" --opt "build-arg:$kv"; done <<EOF
${BUILD_VARS:-}
EOF
[ $# -eq 0 ] || step "build arguments: $(printf '%s\n' "${BUILD_VARS:-}" | sed 's/=.*//' | tr '\n' ' ')"
exec buildctl-daemonless.sh build --progress=plain --frontend dockerfile.v0 \
  --local context=. --local dockerfile="$dfdir" --opt filename="$dfname" \
  --import-cache "$CACHE" --export-cache "$CACHE,mode=max" --output "$OUTPUT" "$@"
