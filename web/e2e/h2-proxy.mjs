// Local stand-in for the HTTPS/HTTP2 gateway: TLS + HTTP/2 to the browser, HTTP/1.1 upstream, streaming bodies.
// Usage: UPSTREAM=localhost:4173 KEY=key.pem CERT=cert.pem [DROP_STREAM=1] node e2e/h2-proxy.mjs  (listens on :8443)
// DROP_STREAM=1 resets every WatchEvents stream after its first frame, as seen in production, to check the
// client backs off instead of looping (see request-count.mjs).
import http from "node:http";
import http2 from "node:http2";
import { readFileSync } from "node:fs";

const [upHost, upPort] = (process.env.UPSTREAM ?? "localhost:4173").split(":");
const server = http2.createSecureServer({ key: readFileSync(process.env.KEY), cert: readFileSync(process.env.CERT), allowHTTP1: true });
server.on("request", (req, res) => {
  const headers = { ...req.headers };
  for (const h of Object.keys(headers)) if (h.startsWith(":")) delete headers[h];
  headers.host = req.headers[":authority"] ?? req.headers.host;
  const up = http.request({ host: upHost, port: Number(upPort), method: req.method, path: req.url, headers }, (ur) => {
    const out = { ...ur.headers };
    for (const h of ["connection", "keep-alive", "transfer-encoding", "upgrade"]) delete out[h];
    res.writeHead(ur.statusCode, out);
    if (process.env.DROP_STREAM && req.url.endsWith("/WatchEvents")) {
      // Mimic production: the first frame arrives, then the stream dies without an end-stream frame.
      ur.once("data", (chunk) => {
        res.write(chunk);
        setTimeout(() => {
          res.stream.close(http2.constants.NGHTTP2_INTERNAL_ERROR);
          ur.destroy();
        }, 20);
      });
      return;
    }
    ur.pipe(res);
  });
  up.on("error", () => res.destroy());
  req.pipe(up);
  res.on("close", () => up.destroy());
});
server.listen(8443, () => console.log("h2 proxy on 8443"));
