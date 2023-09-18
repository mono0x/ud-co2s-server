import { ReadlineParser, SerialPort } from "serialport";
import { exit } from "process";
import { parseArgs } from "util";
import http from "http";

const { values } = parseArgs({
  options: {
    device: {
      type: "string",
      short: "d",
    },
    host: {
      type: "string",
      short: "h",
    },
    port: {
      type: "string",
      short: "p",
    },
  },
  strict: true,
});

const device = values.device;
if (!device) {
  console.error("Device is not specified");
  exit(1);
}
const port = parseInt(values.port, 10);
if (!port) {
  console.error("Listening port is not specified");
  exit(1);
}
const host = values.host;

console.log("Opening serial port");

const serialport = new SerialPort({
  path: device,
  baudRate: 115200,
});
const parser = serialport.pipe(new ReadlineParser());

const sendCommand = async (cmd) => {
  return await new Promise((resolve, reject) => {
    const onData = (data) => {
      if (data.startsWith("OK")) {
        parser.removeListener("data", onData);
        resolve();
      } else if (data.startsWith("NG")) {
        console.log("Received NG:", cmd);
        parser.removeListener("data", onData);
        reject();
      }
    };
    parser.on("data", onData);
    serialport.write(`${cmd}\r\n`);
  });
};

// https://github.com/northeye/chissoku/blob/ad31aa9f2b8086f41bf479606407cae544d5d172/main.go#L43-L57
await (async () => {
  for (const cmd of ["STP", "ID?", "STA"]) {
    await sendCommand(cmd);
  }
})();

let latest = null;

// https://blog.mono0x.net/2023/09/03/ud-co2s-temperature-and-humidity/
const correctTemperature = (rawTemperature) => {
  return rawTemperature - 4.5;
};

const correctHumidity = (rawHumidity, rawTemperature, temperature) => {
  return rawHumidity *
    Math.pow(10, (7.5 * rawTemperature) / (rawTemperature + 237.3)) /
    Math.pow(10, (7.5 * temperature) / (temperature + 237.3));
};

const promise = new Promise((resolve, reject) => {
  const onData = (data) => {
    if (data.startsWith("OK STP")) {
      parser.removeListener("data", onData);
      resolve();
    } else {
      const match = data.match(
        /CO2=(?<co2>\d+),HUM=(?<humidity>[0-9\.]+),TMP=(?<temperature>[0-9\.-]+)/,
      );
      if (match) {
        const co2 = parseInt(match.groups.co2, 10);
        const rawHumidity = parseFloat(match.groups.humidity);
        const rawTemperature = parseFloat(match.groups.temperature);
        const temperature = correctTemperature(rawTemperature);
        const humidity = correctHumidity(
          rawHumidity,
          rawTemperature,
          temperature,
        );
        const timestamp = (new Date()).toISOString();

        if (co2 && humidity && temperature) {
          latest = {
            co2,
            humidity,
            temperature,
            timestamp,
          };
        }
      }
    }
  };
  parser.on("data", onData);
});

console.log("Starting HTTP server");

const server = http.createServer((req, res) => {
  res.writeHead(200, { "Content-Type": "application/json" });
  res.end(JSON.stringify(latest));
});
server.listen(port, host);

process.on("SIGINT", async () => {
  serialport.write("STP\r\n");
  await promise;
  serialport.close();
  exit(0);
});

console.log(`Listening on ${values.host || ""}:${values.port}`);
