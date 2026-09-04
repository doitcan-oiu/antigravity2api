import { Navigate, Route, Routes } from "react-router-dom";
import { getToken } from "./lib/api";
import Layout from "./components/Layout";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Batches from "./pages/Batches";
import Accounts from "./pages/Accounts";
import ImportPage from "./pages/Import";
import Monitor from "./pages/Monitor";
import SettingsPage from "./pages/Settings";
import Routing from "./pages/Routing";

function Guard({ children }: { children: React.ReactNode }) {
  if (!getToken()) return <Navigate to="/login" replace />;
  return children;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="/"
        element={
          <Guard>
            <Layout />
          </Guard>
        }
      >
        <Route index element={<Dashboard />} />
        <Route path="batches" element={<Batches />} />
        <Route path="accounts" element={<Accounts />} />
        <Route path="import" element={<ImportPage />} />
        <Route path="routes" element={<Routing />} />
        <Route path="monitor" element={<Monitor />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
    </Routes>
  );
}
