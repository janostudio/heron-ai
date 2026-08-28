import http from "node:http";
import { appConfig } from "./src/config.js";

const configuredPort = process.env.PORT === undefined
  ? appConfig.port
  : Number(process.env.PORT);

const server = http.createServer((request, response) => {
  const url = new URL(request.url, `http://${request.headers.host}`);

  if (url.pathname === "/health") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({
      status: "ok",
      service: appConfig.name,
      port: server.address().port,
    }));
    return;
  }

  if (url.pathname === "/greeting") {
    const name = url.searchParams.get("name") || "Agent";
    response.writeHead(200, { "content-type": "text/plain; charset=utf-8" });
    response.end(`${appConfig.greeting}, ${name}!`);
    return;
  }

  response.writeHead(404, { "content-type": "text/plain" });
  response.end("not found");
});

server.listen(configuredPort, "127.0.0.1", () => {
  const address = server.address();
  console.log(`SERVICE_READY http://127.0.0.1:${address.port}`);
});

process.on("SIGTERM", () => {
  server.close(() => process.exit(0));
});
