// Texts the hedger sends as codes and parameters (hedger/text.go). The English
// formats mirror the hedger's; the hedger also sends its English rendering,
// which is shown when a code is unknown here (a newer hedger).
//
// Parameter values that are themselves words the hedger chose (a side, a
// state, an action kind, an order role, a group status) are translated too.

import type { Lang } from "./index";

export interface HedgerText {
  code?: string;
  params?: Record<string, string>;
  message: string;
  reason?: HedgerText;
  also?: HedgerText;
}

type Formats = Record<string, string>;

const en: Formats = {
  "invalid.unknownInstrument": "unknown instrument {inst}",
  "invalid.noBook": "no order book for {inst} yet; try again in a moment",
  "invalid.textTooLong": "label or note is too long",
  "invalid.legUnknownInst": "leg {leg}: unknown instrument {inst}",
  "invalid.legUnknownAccount": "leg {leg}: unknown account {account}",
  "invalid.legInactive": "leg {leg}: account {account} is not active",
  "invalid.legMarket": "leg {leg}: account {account} does not trade {market}",
  "invalid.legRoute": "leg {leg}: account {account} cannot trade {inst}",
  "invalid.legSide": "leg {leg}: side must be buy or sell",
  "invalid.legSpotShort": "leg {leg}: a spot leg can only be long (shorting spot needs margin borrowing)",
  "invalid.legRules": "leg {leg}: instrument rules are incomplete",
  "invalid.makerLeg": "maker leg must be 0 or 1",
  "invalid.limitPrice": "limit mode needs a positive limit price for the maker leg",
  "invalid.tif": "tif must be gtc or post_only",
  "invalid.mode": "mode must be market or limit",
  "invalid.sameLegs": "the two legs are the same instrument on the same account",
  "invalid.sameSide": "both legs are {side}: a hedge is one long and one short leg",
  "invalid.policy": "policy must be unwind or hold",
  "invalid.cannotClose": "a {status} group cannot be closed now",
  "invalid.fraction": "fraction must be in (0, 1]",
  "invalid.nothingToClose": "nothing to close at this fraction",
  "invalid.makerNothing": "the maker leg has nothing to close",
  "invalid.busy": "the group already has an action working",
  "invalid.cannotRehedge": "a {status} group cannot be rehedged now",
  "invalid.balanced": "the legs are balanced",
  "invalid.belowStep": "the imbalance is below the smaller leg's step",
  "invalid.cannotTrim": "a {status} group cannot be trimmed now",
  "invalid.nothingToTrim": "nothing to trim",
  "invalid.cannotUnwind": "a {status} group cannot be unwound now",
  "invalid.nothingHeld": "nothing is held",
  "invalid.noWorking": "no working action to cancel",
  "invalid.sizePositive": "size must be positive",
  "invalid.noPrice": "no price for leg {leg} ({inst})",
  "invalid.unit": "unit must be base or quote",
  "invalid.ratio": "ratio must be positive",
  "invalid.match": "match must be base or notional",
  "sizing.roundsToZero": "leg {leg}: size rounds to zero at step {step}",
  "sizing.belowMinQty": "leg {leg}: {qty} is below the minimum size {min}",
  "sizing.belowMinNotional": "leg {leg}: value {value} is below the minimum {min} {quote}",
  "problem.unbalanced": "legs unbalanced: leg 1 holds {held0}, leg 2 holds {held1} (needs {need})",
  "problem.lateFill": "a fill arrived after its action finished",
  "problem.venueShort": "leg {leg}: the venue holds {venue} {base} on {inst}, the hedge accounts for {held}; closed outside the hedge, liquidated or auto-deleveraged",
  "failure.legFilled": "leg {leg} filled {filled} of {target}",
  "failure.notRecorded": "could not record the order: {error}",
  "failure.unresolved": "leg {leg} order outcome could not be established",
  "failure.hedgeRejected": "hedge on leg {leg} rejected",
  "failure.legRejected": "leg {leg} rejected",
  "failure.canceledEarly": "canceled before the maker order was placed",
  "failure.byOperator": "canceled by the operator",
  "failure.makerEnded": "maker order {state}",
  "failure.makerStopped": "maker order {state} after filling {filled} of {target}",
  "reason.rejected": "rejected by the exchange",
  "reason.outcomeUnknown": "outcome unknown",
  "reason.unresolved": "the exchange can neither show nor deny this order and no fill of it was found; check the venue before acting",
  "reason.notFound": "the exchange has no such order; it was never placed",
  "event.opening": "Opening: {side0} {qty0} {inst0} / {side1} {qty1} {inst1} (ratio {ratio}, {mode} mode)",
  "event.closing": "Closing {pct}%: leg 1 {qty0}, leg 2 {qty1} ({mode} mode)",
  "event.unwinding": "Unwinding: leg 1 {qty0}, leg 2 {qty1} at market",
  "event.rehedging": "Rehedging",
  "event.trimming": "Trimming to the matched size",
  "event.target": "leg {leg} {side} {qty}",
  "event.late": "Late venue update: status {from} → {to}",
  "event.broken": "Hedge broken",
  "event.repairSkipped": "Automatic repair skipped: an order's outcome is unknown; check the venue position, then Rehedge, Trim or Unwind",
  "event.trimNotStarted": "Automatic trim not started",
  "event.unhedged": "Unhedged. Check the venue, then Rehedge, Trim or Unwind",
  "event.riskOrder": "{origin} order on leg {leg} ({inst}): {side} {qty}, status {status}",
  "event.cancelRequested": "Cancel requested",
  "event.resumed": "Resumed {kind} after restart; reconciling orders with the venues",
  "event.makerFilled": "Maker leg {leg} filled {filled} of {target}",
  "event.residual": "Residual {qty} on leg {leg} is below its minimum tradable size and stays unhedged",
  "event.notSent": "Order for leg {leg} not sent: the database refused it ({error})",
  "event.sending": "{role} {type} {side} {qty} on leg {leg} {inst} (client {client})",
  "event.sendingAt": "{role} {type} {side} {qty} on leg {leg} {inst} @ {price} (client {client})",
  "event.cancelFailed": "Cancel of the maker order failed ({error}); checking its state",
  "event.unknown": "Leg {leg} order outcome unknown; checking with the exchange, it will not be placed again",
  "event.orderState": "Leg {leg} {side} order {state}",
  "event.orderRejected": "Leg {leg} {side} order rejected",
  "event.progress": "Leg {leg} {side} {state}: {filled}/{qty} filled @ {price}",
  "event.progressNoPrice": "Leg {leg} {side} {state}: {filled}/{qty} filled",
  "event.done": "{kind} done",
  "event.canceled": "{kind} canceled",
  "event.failed": "{kind} failed",
};

const zh: Formats = {
  "invalid.unknownInstrument": "未知标的 {inst}",
  "invalid.noBook": "{inst} 暂无盘口，请稍后再试",
  "invalid.textTooLong": "标签或备注过长",
  "invalid.legUnknownInst": "腿 {leg}：未知标的 {inst}",
  "invalid.legUnknownAccount": "腿 {leg}：未知账户 {account}",
  "invalid.legInactive": "腿 {leg}：账户 {account} 未启用",
  "invalid.legMarket": "腿 {leg}：账户 {account} 不能交易 {market}",
  "invalid.legRoute": "腿 {leg}：账户 {account} 无法交易 {inst}",
  "invalid.legSide": "腿 {leg}：方向必须是买入或卖出",
  "invalid.legSpotShort": "腿 {leg}：现货腿只能做多（做空现货需要借币）",
  "invalid.legRules": "腿 {leg}：标的交易规则不完整",
  "invalid.makerLeg": "挂单腿必须是 0 或 1",
  "invalid.limitPrice": "限价模式需要为挂单腿提供正的限价",
  "invalid.tif": "有效方式必须是 GTC 或只做 Maker",
  "invalid.mode": "模式必须是市价或限价",
  "invalid.sameLegs": "两条腿是同一账户上的同一个标的",
  "invalid.sameSide": "两条腿都是{side}：对冲必须一多一空",
  "invalid.policy": "失败策略必须是回退或保留",
  "invalid.cannotClose": "{status}状态的对冲现在不能平仓",
  "invalid.fraction": "平仓比例必须在 (0, 1] 之间",
  "invalid.nothingToClose": "按此比例没有可平的数量",
  "invalid.makerNothing": "挂单腿没有可平的数量",
  "invalid.busy": "该对冲已有动作在执行",
  "invalid.cannotRehedge": "{status}状态的对冲现在不能补腿",
  "invalid.balanced": "两条腿已经平衡",
  "invalid.belowStep": "不平衡数量小于较小一条腿的步长",
  "invalid.cannotTrim": "{status}状态的对冲现在不能减腿",
  "invalid.nothingToTrim": "没有可减的数量",
  "invalid.cannotUnwind": "{status}状态的对冲现在不能全平",
  "invalid.nothingHeld": "没有持仓",
  "invalid.noWorking": "没有执行中的动作可撤销",
  "invalid.sizePositive": "数量必须大于 0",
  "invalid.noPrice": "腿 {leg}（{inst}）暂无价格",
  "invalid.unit": "单位必须是币或计价货币",
  "invalid.ratio": "比例必须大于 0",
  "invalid.match": "匹配方式必须是按数量或按名义价值",
  "sizing.roundsToZero": "腿 {leg}：按步长 {step} 取整后数量为 0",
  "sizing.belowMinQty": "腿 {leg}：数量 {qty} 低于最小下单量 {min}",
  "sizing.belowMinNotional": "腿 {leg}：金额 {value} 低于最小金额 {min} {quote}",
  "problem.unbalanced": "两腿不平衡：腿 1 持有 {held0}，腿 2 持有 {held1}（应为 {need}）",
  "problem.lateFill": "动作结束后又收到一笔成交",
  "problem.venueShort": "腿 {leg}：交易所在 {inst} 上持有 {venue} {base}，对冲记录为 {held}；可能在对冲之外被平仓、强平或自动减仓",
  "failure.legFilled": "腿 {leg} 成交 {filled} / {target}",
  "failure.notRecorded": "订单未能写入数据库：{error}",
  "failure.unresolved": "腿 {leg} 订单结果无法确认",
  "failure.hedgeRejected": "腿 {leg} 的对冲单被拒",
  "failure.legRejected": "腿 {leg} 被拒",
  "failure.canceledEarly": "挂单下出前已撤销",
  "failure.byOperator": "已由操作员撤销",
  "failure.makerEnded": "挂单{state}",
  "failure.makerStopped": "挂单在成交 {filled} / {target} 后{state}",
  "reason.rejected": "交易所拒绝",
  "reason.outcomeUnknown": "结果未知",
  "reason.unresolved": "交易所既无法显示也无法否认该订单，且未找到它的成交；操作前请先在交易所核对",
  "reason.notFound": "交易所没有该订单，它从未被下出",
  "event.opening": "开仓：{side0} {qty0} {inst0} / {side1} {qty1} {inst1}（比例 {ratio}，{mode}模式）",
  "event.closing": "平仓 {pct}%：腿 1 {qty0}，腿 2 {qty1}（{mode}模式）",
  "event.unwinding": "全平：腿 1 {qty0}，腿 2 {qty1}，市价",
  "event.rehedging": "补腿",
  "event.trimming": "减腿至匹配数量",
  "event.target": "腿 {leg} {side} {qty}",
  "event.late": "交易所延迟更新：状态 {from} → {to}",
  "event.broken": "对冲失衡",
  "event.repairSkipped": "已跳过自动修复：有订单结果未知；请先核对交易所持仓，再补腿、减腿或全平",
  "event.trimNotStarted": "自动减腿未启动",
  "event.unhedged": "未对冲。请核对交易所，然后补腿、减腿或全平",
  "event.riskOrder": "腿 {leg}（{inst}）出现{origin}订单：{side} {qty}，状态 {status}",
  "event.cancelRequested": "已请求撤单",
  "event.resumed": "重启后恢复{kind}，正在与交易所核对订单",
  "event.makerFilled": "挂单腿 {leg} 已成交 {filled} / {target}",
  "event.residual": "腿 {leg} 剩余 {qty} 低于最小下单量，保持未对冲",
  "event.notSent": "腿 {leg} 的订单未发送：数据库拒绝写入（{error}）",
  "event.sending": "{role}{type}{side} {qty}，腿 {leg} {inst}（client {client}）",
  "event.sendingAt": "{role}{type}{side} {qty}，腿 {leg} {inst} @ {price}（client {client}）",
  "event.cancelFailed": "撤销挂单失败（{error}），正在查询其状态",
  "event.unknown": "腿 {leg} 订单结果未知，正在向交易所查询，不会重复下单",
  "event.orderState": "腿 {leg} {side}订单{state}",
  "event.orderRejected": "腿 {leg} {side}订单被拒",
  "event.progress": "腿 {leg} {side} {state}：已成交 {filled}/{qty} @ {price}",
  "event.progressNoPrice": "腿 {leg} {side} {state}：已成交 {filled}/{qty}",
  "event.done": "{kind}完成",
  "event.canceled": "{kind}已撤销",
  "event.failed": "{kind}失败",
};

// Words the hedger passes as parameters.
const words: Record<Lang, Record<string, Record<string, string>>> = {
  en: {
    side: { buy: "buy", sell: "sell" },
    state: { sending: "sending", pending: "pending", open: "open", partially_filled: "partially filled", filled: "filled", canceled: "canceled", rejected: "rejected", unknown: "unknown", not_found: "not found", unresolved: "unresolved" },
    kind: { open: "Open", close: "Close", unwind: "Unwind", rehedge: "Rehedge" },
    role: { maker: "Maker", taker: "Taker", hedge: "Hedge" },
    type: { market: "market", limit: "limit" },
    mode: { market: "market", limit: "limit" },
    status: { opening: "opening", open: "open", closing: "closing", closed: "closed", failed: "failed", broken: "unhedged" },
    origin: { liquidation: "Liquidation", adl: "Auto-deleverage", settlement: "Delisting" },
  },
  zh: {
    side: { buy: "买入", sell: "卖出" },
    state: { sending: "发送中", pending: "待确认", open: "挂单中", partially_filled: "部分成交", filled: "已成交", canceled: "已撤销", rejected: "被拒", unknown: "结果未知", not_found: "未下出", unresolved: "无法确认" },
    kind: { open: "开仓", close: "平仓", unwind: "全平", rehedge: "补腿" },
    role: { maker: "挂单", taker: "吃单", hedge: "对冲" },
    type: { market: "市价单", limit: "限价单" },
    mode: { market: "市价", limit: "限价" },
    status: { opening: "开仓中", open: "已对冲", closing: "平仓中", closed: "已平仓", failed: "失败", broken: "未对冲" },
    origin: { liquidation: "强平", adl: "自动减仓", settlement: "下架结算" },
  },
};

/** Which word list a parameter name draws from (side0 -> side, from -> status). */
function wordList(name: string): string | undefined {
  const base = name.replace(/\d+$/, "");
  if (base === "from" || base === "to") return "status";
  return base in words.en ? base : undefined;
}

function fill(format: string, params: Record<string, string> | undefined, lang: Lang): string {
  return format.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = params?.[name];
    if (value === undefined) return match;
    const list = wordList(name);
    return (list && words[lang][list]?.[value]) ?? value;
  });
}

/**
 * A hedger text in lang: the coded part translated, a venue's own words as
 * written, the reason after a colon and a further text after a semicolon.
 */
export function hedgerText(text: HedgerText | string | undefined | null, lang: Lang): string {
  if (!text) return "";
  if (typeof text === "string") return text;
  const formats = lang === "zh" ? zh : en;
  const format = text.code ? formats[text.code] : undefined;
  // Without a known code, the English message already contains the reason.
  if (!format) return text.message;
  let out = fill(format, text.params, lang);
  if (text.reason) out += (lang === "zh" ? "：" : ": ") + hedgerText(text.reason, lang);
  if (text.also) out += (lang === "zh" ? "；" : "; ") + hedgerText(text.also, lang);
  return out;
}

export const HEDGER_CODES = { en, zh };
