# @pyyol-lens/nodejs

TypeScript client for [Pyyol Eye](https://trace.pyyol.com) (Lens ingest).

```ts
import { initPyyolLens } from "@pyyol-lens/nodejs";

const lens = initPyyolLens({
  apiKey: process.env.PYYOL_LENS_API_KEY!,
  endpoint: "https://trace.pyyol.com",
  serviceName: "my-agent",
  project: "arena",
  environment: "production",
});

await lens.traced("decide", async () => {
  return { card: 1 };
});
await lens.shutdown();
```

Events are POSTed to `{endpoint}/v1/events/batch` with header `X-Pyyol-Key`.
