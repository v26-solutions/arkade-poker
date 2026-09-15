// The pinned renderer probes Node's fs/promises before falling back to Fetch.
// Its distributed browser JS references this Vite external module, but the
// upstream archive omits it. Match the browser stub's failure explicitly so the
// renderer proceeds to Fetch without an unresolved dynamic-import request.
export async function readFile() {
  throw new Error('Local filesystem access is unavailable in the browser');
}
