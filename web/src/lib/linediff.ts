export interface DiffLine {
  kind: "same" | "add" | "del";
  text: string;
}

/**
 * A line diff of a and b (longest common subsequence), for comparing
 * revisions' specs. Inputs are small (a few hundred lines), so the
 * quadratic table is fine.
 */
export function lineDiff(a: string, b: string): DiffLine[] {
  const x = a.split("\n");
  const y = b.split("\n");
  const n = x.length;
  const m = y.length;
  const w = m + 1;
  // lcs[i*w+j]: the longest common subsequence of x[i:] and y[j:].
  const lcs = new Int32Array((n + 1) * w);
  const at = (i: number, j: number) => lcs[i * w + j]!;
  for (let i = n - 1; i >= 0; i--)
    for (let j = m - 1; j >= 0; j--)
      lcs[i * w + j] =
        x[i] === y[j]
          ? at(i + 1, j + 1) + 1
          : Math.max(at(i + 1, j), at(i, j + 1));
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (x[i] === y[j]) {
      out.push({ kind: "same", text: x[i]! });
      i++;
      j++;
    } else if (at(i + 1, j) >= at(i, j + 1)) {
      out.push({ kind: "del", text: x[i++]! });
    } else {
      out.push({ kind: "add", text: y[j++]! });
    }
  }
  while (i < n) out.push({ kind: "del", text: x[i++]! });
  while (j < m) out.push({ kind: "add", text: y[j++]! });
  return out;
}

/** Keeps changed lines with `context` unchanged lines around them. */
export function withContext(
  lines: DiffLine[],
  context = 3,
): (DiffLine | null)[] {
  const keep = lines.map(() => false);
  lines.forEach((l, i) => {
    if (l.kind === "same") return;
    for (
      let k = Math.max(0, i - context);
      k <= Math.min(lines.length - 1, i + context);
      k++
    )
      keep[k] = true;
  });
  const out: (DiffLine | null)[] = [];
  lines.forEach((l, i) => {
    if (keep[i]) out.push(l);
    else if (out.at(-1) !== null) out.push(null); // a gap
  });
  if (out.at(-1) === null) out.pop();
  return out;
}
