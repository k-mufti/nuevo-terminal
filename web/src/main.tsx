import { render } from "solid-js/web";
import App from "./App";
// Self-hosted (bundled) so no third-party request blocks startup.
import "@fontsource/ibm-plex-mono/latin-400.css";
import "@fontsource/ibm-plex-mono/latin-500.css";
import "@fontsource/ibm-plex-mono/latin-600.css";
import "./styles.css";

render(() => <App />, document.getElementById("root")!);
