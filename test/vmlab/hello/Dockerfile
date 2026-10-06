FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
ARG VERSION=v1
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /hello .

FROM scratch
COPY --from=build /hello /hello
ENV VERSION=v1
EXPOSE 8080
ENTRYPOINT ["/hello"]
