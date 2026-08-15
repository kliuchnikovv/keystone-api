/**
 * Test helpers, published at the "./testing" subpath.
 *
 * Kept out of the main entry point so nothing here is linked into a plugin
 * that only imports the runtime.
 */
import { createServer, connect, type Socket } from "node:net";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

export interface SocketPair {
  a: Socket;
  b: Socket;
  close(): Promise<void>;
}

/**
 * A pair of connected unix sockets.
 *
 * Node has no socketpair(2) binding, so this listens on a socket in a temp
 * directory and dials it. Real sockets rather than in-memory streams keep a
 * test on the same code path as production — close, destroy and backpressure
 * all behave as they will in the field, and an in-memory Duplex composite does
 * not (it raises an AbortError from destroy that a real socket never does).
 */
export async function socketPair(): Promise<SocketPair> {
  // Short path: unix socket names are capped around 104 bytes on darwin.
  const dir = await mkdtemp(join(tmpdir(), "ks"));
  const path = join(dir, "p.sock");

  const server = createServer();
  await new Promise<void>((resolve) => server.listen(path, resolve));

  const accepted = new Promise<Socket>((resolve) => server.once("connection", resolve));
  const a = connect(path);
  await new Promise<void>((resolve, reject) => {
    a.once("connect", () => resolve());
    a.once("error", reject);
  });
  const b = await accepted;

  return {
    a,
    b,
    async close() {
      a.destroy();
      b.destroy();
      await new Promise<void>((resolve) => server.close(() => resolve()));
      await rm(dir, { recursive: true, force: true });
    },
  };
}
