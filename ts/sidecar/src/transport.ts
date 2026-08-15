/**
 * Unix socket helpers (spec §3).
 */
import { createServer, connect, type Server, type Socket } from "node:net";
import { chmod, mkdir, lstat, unlink } from "node:fs/promises";
import { dirname } from "node:path";

/** The environment variable the core uses to tell a plugin where its socket
 * lives. */
export const SOCKET_ENV_VAR = "KEYSTONE_PLUGIN_SOCKET";

/** The socket path the core assigned. */
export function socketPathFromEnv(): string {
  const path = process.env[SOCKET_ENV_VAR];
  if (!path) {
    throw new Error(
      `sidecar: ${SOCKET_ENV_VAR} is not set — a plugin is started by the core, which supplies it`,
    );
  }
  return path;
}

/** Connects to a socket. */
export function dialUnix(path: string): Promise<Socket> {
  return new Promise((resolve, reject) => {
    const socket = connect(path);
    socket.once("connect", () => resolve(socket));
    socket.once("error", reject);
  });
}

/**
 * Creates a listening socket.
 *
 * Removes a stale socket file first: one left behind by a killed process makes
 * bind fail with EADDRINUSE for a socket nothing is listening on, and a plugin
 * restarted after a crash would never come back. The check is narrow — only a
 * socket file is removed, and only if nothing answers on it.
 *
 * Mode 0600, so it is reachable only by the user the plugin runs as. That is
 * the whole of v1's access control; see OPEN_QUESTIONS.md §1.
 */
export async function listenUnix(path: string): Promise<Server> {
  await mkdir(dirname(path), { recursive: true });
  await removeStaleSocket(path);

  const server = createServer();
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(path, () => {
      server.removeListener("error", reject);
      resolve();
    });
  });

  await chmod(path, 0o600);
  return server;
}

async function removeStaleSocket(path: string): Promise<void> {
  let stat;
  try {
    stat = await lstat(path);
  } catch {
    return; // nothing there
  }

  if (!stat.isSocket()) {
    // Something that is not a socket is in the way. Removing it would be
    // overreach — it might be a file someone cares about.
    throw new Error(`sidecar: ${path} exists and is not a socket`);
  }

  // If a live process is listening, this is a genuine conflict rather than a
  // leftover, and unlinking would steal the socket from a running plugin.
  const live = await new Promise<boolean>((resolve) => {
    const probe = connect(path);
    const done = (answer: boolean) => {
      probe.destroy();
      resolve(answer);
    };
    probe.once("connect", () => done(true));
    probe.once("error", () => done(false));
    setTimeout(() => done(false), 200).unref?.();
  });

  if (live) throw new Error(`sidecar: ${path} is already in use by a live listener`);
  await unlink(path);
}
