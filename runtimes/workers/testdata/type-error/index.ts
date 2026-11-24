export default {
  async fetch(): Promise<Response> {
    const status: string = 500;
    return new Response(status);
  },
} satisfies ExportedHandler;
