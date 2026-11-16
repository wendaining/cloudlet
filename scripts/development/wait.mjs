import waitOn from 'wait-on';

await waitOn({
  resources: process.argv.slice(2),
  timeout: 120_000,
  interval: 300,
  httpTimeout: 2000,
  proxy: false,
});
