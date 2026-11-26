export default {
  fetch(request) {
    const path = new URL(request.url).pathname;
    if (path !== "/stream/full" && path !== "/stream/abort") {
      return new Response("ready");
    }

    const mode = path === "/stream/abort" ? "abort" : "full";
    const encoder = new TextEncoder();

    request.signal.addEventListener("abort", () => {
      console.log(`cloudlet-stream:${mode}:request-aborted`);
    });
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode("first\n"));
        console.log(`cloudlet-stream:${mode}:first`);
      },
      async pull(controller) {
        await new Promise((resolve) => setTimeout(resolve, 5_000));
        controller.enqueue(encoder.encode("last\n"));
        console.log(`cloudlet-stream:${mode}:completed`);
        controller.close();
      },
      cancel() {
        console.log(`cloudlet-stream:${mode}:body-cancelled`);
      },
    });
    return new Response(stream, { headers: { "Content-Type": "text/plain; charset=utf-8" } });
  },
};
