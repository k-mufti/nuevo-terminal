// Shapes sent by the Go server. Times are Unix milliseconds.

export interface Item {
  src: string;
  pub: string;
  title: string;
  link: string;
  time: number;
}

export interface Cluster {
  id: string;
  title: string;
  link: string;
  src: string;
  pub: string;
  summary?: string;
  time: number; // first report
  upd: number; // latest report
  cats: string[];
  n: number; // distinct publishers
  brk: boolean;
  brkAt?: number;
  items?: Item[]; // every story in the cluster, lead first; only when more than one
  more?: Item[]; // on the wire: the stories after the lead (becomes items)
}

export interface Quote {
  sym: string;
  name: string;
  price: number;
  change: number;
  pct: number;
  time: number;
  spark?: number[];
}

export interface Status {
  feedsOk: number;
  feedsTotal: number;
  stories: number;
  clusters: number;
  clients: number;
}

export interface PanelDef {
  code: string; // what you type in the command bar
  aliases: string[];
  cat: string; // server category
  title: string;
  solo?: boolean; // full-screen only, not shown in the grid
}
