import { api, newClientOrderId } from "@/api/client";
import type { Order, PlaceRequest, PlaceResult, Trigger, TriggerRequest, TriggerResult } from "@/api/types";
import { translate } from "@/i18n";

export type Outcome =
  | { kind: "accepted"; order: Order }
  | { kind: "rejected"; message: string }
  | { kind: "unknown"; message: string };

export interface SubmitDeps {
  place: typeof api.place;
  lookup: typeof api.lookup;
  sleep: (ms: number) => Promise<void>;
}

const defaultDeps: SubmitDeps = {
  place: api.place,
  lookup: api.lookup,
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
};

/**
 * Places one order and resolves it to a definite outcome when possible.
 *
 * The clientOrderId is generated once per submission. If the gateway reports
 * an unknown outcome (or the request itself fails in transit), we never place
 * again; we look the order up by that clientOrderId a few times instead.
 */
export async function submitOrder(
  accountId: string,
  order: Omit<PlaceRequest, "clientOrderId">,
  deps: SubmitDeps = defaultDeps,
): Promise<Outcome> {
  const clientOrderId = newClientOrderId();
  let result: PlaceResult | undefined;
  try {
    result = await deps.place(accountId, { ...order, clientOrderId });
  } catch (error) {
    // A 4xx before the venue (validation, auth) is a definite refusal.
    const status = (error as { status?: number }).status;
    if (status && status >= 400 && status < 500) {
      return { kind: "rejected", message: (error as Error).message };
    }
    // Network failure or 5xx: the order may or may not have been sent.
  }
  if (result?.status === "accepted" && result.order) return { kind: "accepted", order: result.order };
  if (result?.status === "rejected") {
    const text = result.message || result.venueMessage || translate("order.rejected");
    return { kind: "rejected", message: result.venueCode && !text.includes(result.venueCode) ? `${text} (${result.venueCode})` : text };
  }
  return resolveUnknown(accountId, order.inst, clientOrderId, deps);
}

async function resolveUnknown(accountId: string, inst: string, clientOrderId: string, deps: SubmitDeps): Promise<Outcome> {
  for (const delay of [500, 1500, 3000]) {
    await deps.sleep(delay);
    try {
      const lookup = await deps.lookup(accountId, inst, clientOrderId);
      if (lookup.status === "found" && lookup.order) return { kind: "accepted", order: lookup.order };
      if (lookup.status === "not_found") return { kind: "rejected", message: translate("order.notPlaced") };
    } catch {
      // Try again.
    }
  }
  return { kind: "unknown", message: translate("order.unknownCheck", { id: clientOrderId }) };
}

export type TriggerOutcome =
  | { kind: "accepted"; trigger: Trigger }
  | { kind: "rejected"; message: string }
  | { kind: "unknown"; message: string };

export interface TriggerDeps {
  place: typeof api.placeTrigger;
  lookup: typeof api.lookupTrigger;
  sleep: (ms: number) => Promise<void>;
}

const defaultTriggerDeps: TriggerDeps = {
  place: api.placeTrigger,
  lookup: api.lookupTrigger,
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
};

/**
 * Places one take-profit or stop-loss with the order rules: one
 * clientOrderId per submission, and an unknown outcome is looked up by it,
 * never sent again.
 */
export async function submitTrigger(accountId: string, trigger: Omit<TriggerRequest, "clientOrderId">, deps: TriggerDeps = defaultTriggerDeps): Promise<TriggerOutcome> {
  const clientOrderId = newClientOrderId();
  let result: TriggerResult | undefined;
  try {
    result = await deps.place(accountId, { ...trigger, clientOrderId });
  } catch (error) {
    const status = (error as { status?: number }).status;
    if (status && status >= 400 && status < 500) return { kind: "rejected", message: (error as Error).message };
  }
  if (result?.status === "accepted" && result.trigger) return { kind: "accepted", trigger: result.trigger };
  if (result?.status === "rejected") {
    const text = result.message || result.venueMessage || translate("order.rejected");
    return { kind: "rejected", message: result.venueCode && !text.includes(result.venueCode) ? `${text} (${result.venueCode})` : text };
  }
  for (const delay of [500, 1500, 3000]) {
    await deps.sleep(delay);
    try {
      const lookup = await deps.lookup(accountId, trigger.inst, clientOrderId);
      if (lookup.status === "found" && lookup.trigger) return { kind: "accepted", trigger: lookup.trigger };
      if (lookup.status === "not_found") return { kind: "rejected", message: translate("order.notPlaced") };
    } catch {
      // Try again.
    }
  }
  return { kind: "unknown", message: translate("order.unknownCheck", { id: clientOrderId }) };
}
