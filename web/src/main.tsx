import "@fontsource-variable/dm-sans/wght.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { Toast } from "@heroui/react";
import App from "./App";
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
      <Toast.Provider placement="top-end" maxVisibleToasts={3} width={360} />
    </BrowserRouter>
  </StrictMode>
);
