export function readViewport(env: NodeJS.ProcessEnv) {
  const width = dimension(env.CLOUDLET_BROWSER_VIEWPORT_WIDTH, 1280, 320, 1920, 'CLOUDLET_BROWSER_VIEWPORT_WIDTH');
  const height = dimension(env.CLOUDLET_BROWSER_VIEWPORT_HEIGHT, 720, 240, 1080, 'CLOUDLET_BROWSER_VIEWPORT_HEIGHT');
  const rawScale = env.CLOUDLET_BROWSER_DEVICE_SCALE_FACTOR ?? '1';
  const deviceScaleFactor = Number(rawScale);
  if (!/^\d+(?:\.\d+)?$/.test(rawScale) || deviceScaleFactor < 1 || deviceScaleFactor > 2) {
    throw new Error('CLOUDLET_BROWSER_DEVICE_SCALE_FACTOR must be a decimal between 1 and 2');
  }
  if (!devicePixelsFit(width, height, deviceScaleFactor)) {
    throw new Error('Browser viewport exceeds 4194304 device pixels');
  }
  return { viewport: { width, height }, deviceScaleFactor };
}

function dimension(value: string | undefined, fallback: number, min: number, max: number, name: string) {
  if (value === undefined) return fallback;
  const size = Number(value);
  if (!/^\d+$/.test(value) || size < min || size > max) {
    throw new Error(`${name} must be an integer between ${min} and ${max}`);
  }
  return size;
}


// Apply the same device-pixel budget to viewport and full-page captures.
export function devicePixelsFit(width: number, height: number, scale: number) {
  if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width < 1 || height < 1 ||
      !Number.isFinite(scale) || scale < 1 || scale > 2) return false;
  return Math.ceil(width * scale) * Math.ceil(height * scale) <= 4_194_304;
}
