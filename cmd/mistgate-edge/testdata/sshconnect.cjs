"use strict";

// The Worker's sshConnect (edge/worker/src/shell.ts: openSocket) for the js tests, over node:net instead of
// cloudflare:sockets, plus a scripted socket for the tests of jsConn itself. Loaded with --require
// (scripts/test-edge-store.ps1); the address of the fake SSH server comes from the environment it prints on start.
//
// The host picks the behaviour, so one test can meet every kind of target:
//   45.67.89.11  refused         45.67.89.12  failed          45.67.89.13  a server that never answers
//   45.67.89.14  opens slowly (after 400 ms, or "timeout" when timeoutMs is shorter)
//   any other    the fake SSH server
//
// globalThis.__sshStats counts the sockets opened and closed, so a test can see that a socket nobody wanted was closed.

const net = require("node:net");

const stats = { opened: 0, closed: 0 };
globalThis.__sshStats = stats;

function split(address) {
  const at = address.lastIndexOf(":");
  return { host: address.slice(0, at), port: Number(address.slice(at + 1)) };
}

/** A node socket as the Worker's socket object: read() resolves null at the end, nothing throws after close(). */
function wrap(socket) {
  const queue = [];
  let ended = false;
  let waiter = null;
  let closed = false;
  stats.opened++;
  const wake = () => {
    if (waiter) {
      const w = waiter;
      waiter = null;
      w();
    }
  };
  socket.on("data", (chunk) => {
    queue.push(new Uint8Array(chunk));
    if (queue.length > 32) socket.pause();
    wake();
  });
  socket.on("end", () => { ended = true; wake(); });
  socket.on("close", () => { ended = true; wake(); });
  socket.on("error", () => { ended = true; wake(); });
  return {
    ok: true,
    async read() {
      while (queue.length === 0 && !ended && !closed) await new Promise((resolve) => { waiter = resolve; });
      if (queue.length === 0) return null;
      const chunk = queue.shift();
      if (queue.length < 8) socket.resume();
      return chunk;
    },
    write(bytes) {
      return new Promise((resolve, reject) => {
        if (closed) return reject(new Error("This socket has been closed"));
        socket.write(bytes, (error) => (error ? reject(error) : resolve()));
      });
    },
    close() {
      if (closed) return;
      closed = true;
      stats.closed++;
      socket.destroy();
      wake();
    },
  };
}

function openFake(address, timeoutMs) {
  const { host, port } = split(address);
  return new Promise((resolve) => {
    const socket = net.connect({ host, port });
    const timer = setTimeout(() => { socket.destroy(); resolve({ ok: false, error: "timeout" }); }, timeoutMs);
    socket.once("connect", () => { clearTimeout(timer); resolve(wrap(socket)); });
    socket.once("error", () => { clearTimeout(timer); resolve({ ok: false, error: "refused" }); });
  });
}

globalThis.__sshConnect = async (host, port, timeoutMs) => {
  const server = process.env.MG_FAKESSHD_ADDR;
  switch (host) {
    case "45.67.89.11":
      return { ok: false, error: "refused" };
    case "45.67.89.12":
      return { ok: false, error: "failed" };
    case "45.67.89.13":
      return openFake(process.env.MG_FAKESSHD_SILENT, timeoutMs);
    case "45.67.89.14":
      if (timeoutMs < 400) {
        await new Promise((resolve) => setTimeout(resolve, timeoutMs));
        return { ok: false, error: "timeout" };
      }
      await new Promise((resolve) => setTimeout(resolve, 400));
      return openFake(server, timeoutMs);
    default:
      return openFake(server, timeoutMs);
  }
};

/**
 * A socket the test drives by hand: push() hands a chunk to the read() waiting for one, end() ends the stream,
 * fail() makes read() reject, and a write() waits for release() when held. close() rejects a read in flight the way a
 * closed Worker socket does ("This socket has been closed"), so jsConn has to swallow that.
 */
globalThis.__scriptedSocket = () => {
  const queue = [];
  let waiter = null;
  let state = null; // null | "end" | Error
  let gate = null;
  const socket = {
    readCalls: 0,
    closeCalls: 0,
    written: [],
    read() {
      socket.readCalls++;
      return new Promise((resolve, reject) => {
        const settle = () => {
          if (queue.length > 0) return resolve(queue.shift());
          if (state === "end") return resolve(null);
          if (state instanceof Error) return reject(state);
          waiter = settle;
        };
        settle();
      });
    },
    write(bytes) {
      socket.written.push(Uint8Array.from(bytes));
      return gate ? gate.promise : Promise.resolve();
    },
    close() {
      socket.closeCalls++;
      socket.fail("This socket has been closed");
    },
    push(bytes) {
      queue.push(Uint8Array.from(bytes));
      if (waiter) { const w = waiter; waiter = null; w(); }
    },
    end() {
      state = "end";
      if (waiter) { const w = waiter; waiter = null; w(); }
    },
    fail(message) {
      state = new Error(message);
      if (waiter) { const w = waiter; waiter = null; w(); }
    },
    hold() {
      let release;
      gate = { promise: new Promise((resolve) => { release = resolve; }) };
      gate.release = release;
    },
    release() {
      if (gate) gate.release();
      gate = null;
    },
  };
  return socket;
};
