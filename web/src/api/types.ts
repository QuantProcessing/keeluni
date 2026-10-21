// Wire types of the gateway /v1 API (see docs/api/gateway-rest.md).

export type Product = "spot" | "perp" | "option";
export type Side = "buy" | "sell";

export interface Instrument {
  id: string;
  venue: string;
  product: Product;
  symbol: string;
  base: string;
  quote: string;
  settle?: string;
  tickSize: string;
  stepSize: string;
  minQty: string;
  minNotional?: string;
  marketFamily?: string;
  /** The venue's classification when it is not plain crypto: equity, commodity, fx, ... */
  assetClass?: string;
  /** A coarser tick applies at prices from coarseTickFrom up (Deribit options). */
  coarseTickFrom?: string;
  coarseTick?: string;
  option?: OptionTerms;
}

/** An option's contract. Sizes are in the underlying; prices in premiumAsset per unit. */
export interface OptionTerms {
  underlying: string;
  kind: "call" | "put";
  strike: string;
  /** Unix ms. */
  expiry: number;
  premiumAsset: string;
  /** Premium and settlement in the coin itself (Deribit BTC, ETH). */
  inverse?: boolean;
  european: boolean;
}

/** An instrument an account can trade, with the rules its orders follow there. */
export interface Route {
  inst: string;
  tickSize: string;
  stepSize: string;
  minQty: string;
  minNotional?: string;
}

/** "native": one venue. "broker": one margin pool trading many venues (Gate CrossEx). */
export type AccountKind = "native" | "broker";

export interface CredentialField {
  key: string;
  label: string;
  metadata?: boolean;
  required: boolean;
  valueType: "string" | "integer";
}

export interface VenueInfo {
  venue: string;
  kind: AccountKind;
  markets: string[];
  fields: CredentialField[];
}

export interface Account {
  id: string;
  venue: string;
  kind: AccountKind;
  label: string;
  createdAt: string;
  markets: string[];
  active: boolean;
}

export type Level = [string, string];

export interface Book {
  bids: Level[];
  asks: Level[];
  ts: number;
}

export interface Trade {
  p: string;
  q: string;
  side: Side;
  ts: number;
}

export interface Candle {
  t: number;
  o: string;
  h: string;
  l: string;
  c: string;
  v: string;
}

export interface Ticker {
  mark?: string;
  index?: string;
  /** Fraction of notional paid at nextFunding (>0: longs pay shorts). */
  funding?: string;
  fundingIntervalHours?: number;
  /** funding annualized (simple, 8760 h); absent when the period is unknown. */
  fundingApr?: string;
  nextFunding?: number;
  oi?: string;
  ts: number;
}

export interface Stats {
  last: string;
  open24h: string;
  high24h: string;
  low24h: string;
  volume24h: string;
  funding?: string;
  fundingIntervalHours?: number;
  fundingApr?: string;
  nextFunding?: number;
  mark?: string;
}

/** One perpetual's current funding from the gateway's resident dataset. */
export interface FundingQuote {
  inst: string;
  venue: string;
  base: string;
  rate: string;
  intervalHours?: number;
  apr?: string;
  nextFunding?: number;
  /** Mark price and open interest in USD, when the venue publishes them. */
  mark?: string;
  oiUsd?: string;
  observedAt: number;
  stale: boolean;
}

export interface FundingPoint {
  t: number;
  rate: string;
  intervalHours?: number;
  apr?: string;
}

export interface Position {
  inst: string;
  side: "long" | "short";
  size: string;
  entryPrice: string;
  markPrice?: string;
  unrealizedPnl: string;
  liquidationPrice?: string;
  leverage?: string;
  margin?: string;
}

export interface Balance {
  market: string;
  asset: string;
  total: string;
  available: string;
  locked: string;
}

export type OrderStatus = "open" | "partially_filled" | "filled" | "canceled" | "rejected" | "pending";

export interface Order {
  inst: string;
  orderId: string;
  clientOrderId?: string;
  side: Side;
  type: "market" | "limit";
  price?: string;
  qty: string;
  filled: string;
  avgPrice?: string;
  tif?: string;
  reduceOnly?: boolean;
  status: OrderStatus;
  createdAt?: number;
  updatedAt?: number;
  /** Why an accepted order was rejected afterwards (asynchronous venues). */
  rejectCode?: string;
  rejectMessage?: string;
  /** Set for orders the venue's risk engine placed. */
  origin?: "liquidation" | "adl" | "settlement";
}

export interface Fill {
  inst: string;
  fillId: string;
  orderId: string;
  clientOrderId?: string;
  side: Side;
  price: string;
  qty: string;
  fee: string;
  feeAsset?: string;
  liquidity?: string;
  ts: number;
}

export interface PlaceRequest {
  inst: string;
  side: Side;
  type: "market" | "limit";
  qty: string;
  price?: string;
  tif?: "gtc" | "ioc" | "post_only";
  reduceOnly?: boolean;
  clientOrderId: string;
}

export interface PlaceResult {
  status: "accepted" | "rejected" | "unknown";
  order?: Order;
  /** Why it was rejected; uniform across venues. */
  category?: "network" | "rejected" | "credentials" | "rate_limited" | "invalid" | "unsupported" | "venue_error";
  venueCode?: string;
  venueMessage?: string;
  /** Display text. For venue rejections this is the venue's own message. */
  message?: string;
}

export interface LookupResult {
  status: "found" | "not_found" | "unconfirmed";
  order?: Order;
}

export interface AccountStatus {
  connected: boolean;
  markets: Record<string, boolean>;
}

/** A broker account's shared margin pool. Rates are ratios: 2.4 means 240%. */
export interface Margin {
  equity?: string;
  available?: string;
  initialMargin?: string;
  maintenanceMargin?: string;
  initialMarginRate?: string;
  maintenanceMarginRate?: string;
  unrealizedPnl?: string;
  byVenue?: VenueMargin[];
  ts: number;
}

export interface VenueMargin {
  venue: string;
  asset: string;
  equity: string;
  available: string;
  initialMargin?: string;
  unrealizedPnl?: string;
}
