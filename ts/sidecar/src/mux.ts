/**
 * Inbound request routing.
 */
import type { Peer } from "./peer.js";
import { ProtocolError, Code } from "./errors.js";

/** An inbound method call. */
export class Request {
  constructor(
    readonly id: string,
    readonly method: string,
    readonly params: unknown,
    /**
     * The connection this arrived on.
     *
     * This is where the protocol's symmetry becomes usable rather than
     * theoretical: a handler can publish or call back while it is still
     * deciding what to answer. A commissioning handler reports progress this
     * way instead of leaving the user watching a spinner.
     */
    readonly peer: Peer,
  ) {}

  /**
   * The parameters, typed.
   *
   * Absent params bind as an empty object rather than failing, so a method
   * whose parameters are all optional works whether the caller sent
   * "params":{} or nothing at all.
   */
  bind<T>(): T {
    return (this.params ?? {}) as T;
  }
}

/** An inbound push. */
export interface Push {
  topic: string;
  seq: number;
  ts: string;
  payload: unknown;
}

export type HandlerFn = (req: Request) => unknown | Promise<unknown>;

/** Routes requests by method name. */
export class Mux {
  #handlers = new Map<string, HandlerFn>();

  /**
   * Registers a handler, replacing any previous one for that method.
   *
   * Replacing rather than throwing on a duplicate is deliberate: runPlugin
   * installs defaults for hello, ping and the subscription methods, and a
   * plugin with a good reason to take one over should simply be able to
   * register its own.
   */
  handle(method: string, fn: HandlerFn): this {
    this.#handlers.set(method, fn);
    return this;
  }

  has(method: string): boolean {
    return this.#handlers.has(method);
  }

  get methods(): string[] {
    return [...this.#handlers.keys()].sort();
  }

  /**
   * Dispatches a request.
   *
   * An unregistered method is rejected with protocol.unsupported rather than
   * ignored: the caller is blocked waiting for a response with this id, and
   * silence would hang it until its own timeout — turning "I do not implement
   * that" into something that reads like a dead plugin.
   */
  async dispatch(req: Request): Promise<unknown> {
    const fn = this.#handlers.get(req.method);
    if (!fn) {
      throw new ProtocolError(
        Code.ProtocolUnsupported,
        `method ${JSON.stringify(req.method)} is not implemented`,
      );
    }
    return await fn(req);
  }
}
