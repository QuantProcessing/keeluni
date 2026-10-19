import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app";
import { migrateStorage } from "./lib/storage";
import "./styles.css";

migrateStorage();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
