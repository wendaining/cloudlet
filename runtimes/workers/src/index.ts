interface Env {}

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    return new Response(`Hello ${url.pathname}`, {
      headers: { "X-Worker": "cloudlet" },
    });
  },
} satisfies ExportedHandler<Env>;
