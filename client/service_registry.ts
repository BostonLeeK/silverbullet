import type { Config } from "./config.ts";
import type { EventHook } from "./plugos/hooks/event.ts";

export type ServiceMatch = {
  id?: string; // uuid, set automatically
  priority?: number;
} & Record<string, any>;

export type ServiceSpec = {
  selector: string;
  match:
    | ServiceMatch
    | ((data: any) => Promise<ServiceMatch | null | undefined>);
  run: (data: any) => Promise<any>;
};

function serviceId(): string {
  if (typeof globalThis.crypto?.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  if (typeof globalThis.crypto?.getRandomValues === "function") {
    const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  const rnd = () => Math.floor(Math.random() * 0x100000000).toString(16).padStart(8, "0");
  return `${rnd()}-${rnd().slice(0, 4)}-4${rnd().slice(1, 4)}-a${rnd().slice(1, 4)}-${rnd()}${rnd().slice(0, 4)}`;
}

export class ServiceRegistry {
  constructor(
    private eventHook: EventHook,
    private config: Config,
  ) {}

  public define(spec: ServiceSpec): void {
    const id = serviceId();
    // Register with discover:* event
    this.config.insert(
      ["eventListeners", `discover:${spec.selector}`],
      async (e: any) => {
        const matchResult =
          typeof spec.match === "function"
            ? await spec.match(e.data)
            : spec.match;
        if (matchResult) {
          return {
            ...matchResult,
            id,
          };
        }
      },
    );
    // Register callback when invoked
    this.config.insert(["eventListeners", `service:${id}`], (e: any) => {
      return spec.run(e.data);
    });
  }

  public async discover(selector: string, opts: any): Promise<ServiceMatch[]> {
    const discoveryResults: ServiceMatch[] = await this.eventHook.dispatchEvent(
      `discover:${selector}`,
      opts,
    );
    discoveryResults.sort((a, b) => (b.priority || 0) - (a.priority || 0));
    return discoveryResults;
  }

  public async invoke(match: ServiceMatch, data: any): Promise<any> {
    const results = await this.eventHook.dispatchEvent(
      `service:${match.id}`,
      data,
    );
    // Note: results may be an empty array in case no service actually returned a result (void) case, which is implicitly passed on here
    return results[0];
  }

  public async invokeBestMatch(selector: string, data: any): Promise<any> {
    const results = await this.discover(selector, data);
    if (results.length === 0) {
      throw new Error(`No services matching: ${selector}`);
    }
    return this.invoke(results[0], data);
  }
}
