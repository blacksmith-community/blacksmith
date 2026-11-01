// Pins the time zone for a test file so timestamp text does not depend on
// the machine that runs it. Call pinZone('America/New_York') at the top of a
// file. The zone is set before the file's tests run and restored after.
const { beforeAll, afterAll } = require('bun:test');

const pinZone = (zone) => {
  let previous;
  beforeAll(() => {
    previous = process.env.TZ;
    process.env.TZ = zone;
  });
  afterAll(() => {
    if (previous === undefined) delete process.env.TZ;
    else process.env.TZ = previous;
  });
};

module.exports = { pinZone };
