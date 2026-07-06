"use strict";

// Bake a client ID in here for a deployed instance (client IDs are public,
// not secrets); otherwise it is taken from the input, persisted in localStorage.
const DEFAULT_CLIENT_ID = "";
const SCOPE =
  "https://www.googleapis.com/auth/presentations " +
  "https://www.googleapis.com/auth/userinfo.email";
const CLIENT_ID_KEY = "svg2gslide.clientId";

const clientIdInput = document.getElementById("clientId");
const loginBtn = document.getElementById("loginBtn");
const loginState = document.getElementById("loginState");
const svgFileInput = document.getElementById("svgFile");
const dropZone = document.getElementById("dropZone");
const presUrlInput = document.getElementById("presUrl");
const convertBtn = document.getElementById("convertBtn");
const statusEl = document.getElementById("status");
const warningsEl = document.getElementById("warnings");

let gisReady = false;
let wasmReady = false;
let selectedFile = null;
let tokenClient = null;
let accessToken = null;
let tokenExpiresAt = 0;
let userEmail = null;
// Set when a convert is waiting for a (re-)issued token.
let pendingConvert = false;

function setStatus(msg, isError) {
  statusEl.textContent = "";
  statusEl.append(msg);
  statusEl.classList.toggle("error", !!isError);
}

function updateButtons() {
  loginBtn.disabled = !(gisReady && clientIdInput.value.trim());
  convertBtn.disabled = !(wasmReady && accessToken && selectedFile);
  if (accessToken) {
    loginBtn.textContent = "Switch account";
    loginBtn.classList.add("signedin");
    loginState.textContent = userEmail ? ` Signed in as ${userEmail} ✓` : " Signed in ✓";
  } else {
    loginBtn.textContent = "Sign in with Google";
    loginBtn.classList.remove("signedin");
    loginState.textContent = "";
  }
}

async function fetchUserEmail() {
  try {
    const resp = await fetch("https://www.googleapis.com/oauth2/v3/userinfo", {
      headers: { Authorization: "Bearer " + accessToken },
    });
    if (resp.ok) {
      userEmail = (await resp.json()).email || null;
      updateButtons();
    }
  } catch (e) {
    // Cosmetic only — the plain "Signed in ✓" stays.
  }
}

// ---- SVG file selection (drag & drop or click-to-browse) --------------------

function setFile(file) {
  if (!file) return;
  const looksSvg = file.type === "image/svg+xml" || /\.svg$/i.test(file.name);
  if (!looksSvg) {
    setStatus(`"${file.name}" doesn't look like an SVG file.`, true);
    return;
  }
  selectedFile = file;
  dropZone.classList.add("hasfile");
  dropZone.textContent = "";
  const name = document.createElement("strong");
  name.textContent = file.name;
  dropZone.append(name, ` (${Math.ceil(file.size / 1024)} KB) — drop or click to replace`);
  updateButtons();
}

dropZone.addEventListener("click", () => svgFileInput.click());
svgFileInput.addEventListener("change", () => setFile(svgFileInput.files[0]));

dropZone.addEventListener("dragover", (e) => {
  e.preventDefault();
  dropZone.classList.add("dragover");
});
dropZone.addEventListener("dragleave", () => dropZone.classList.remove("dragover"));
dropZone.addEventListener("drop", (e) => {
  e.preventDefault();
  dropZone.classList.remove("dragover");
  setFile(e.dataTransfer.files[0]);
});
// A drop outside the zone must not make the browser navigate to the file.
window.addEventListener("dragover", (e) => e.preventDefault());
window.addEventListener("drop", (e) => e.preventDefault());

// ---- WebAssembly boot -------------------------------------------------------

const go = new Go();
async function loadWasm() {
  try {
    let result;
    try {
      result = await WebAssembly.instantiateStreaming(fetch("main.wasm"), go.importObject);
    } catch (e) {
      // Fallback for servers that don't send Content-Type: application/wasm.
      const buf = await (await fetch("main.wasm")).arrayBuffer();
      result = await WebAssembly.instantiate(buf, go.importObject);
    }
    go.run(result.instance); // resolves only when the Go program exits
  } catch (e) {
    setStatus("Failed to load main.wasm: " + e, true);
  }
}

// Called by the Go side once svg2gslideConvert is registered.
window.onSvg2gslideReady = () => {
  wasmReady = true;
  setStatus("Ready. Sign in, pick an SVG and a presentation, then convert.");
  updateButtons();
};

loadWasm();

// ---- Google Identity Services (OAuth token flow) ----------------------------

function gisLoaded() {
  gisReady = true;
  updateButtons();
}
window.gisLoaded = gisLoaded; // the GIS <script onload> calls this

function initTokenClient() {
  const clientId = clientIdInput.value.trim();
  tokenClient = google.accounts.oauth2.initTokenClient({
    client_id: clientId,
    scope: SCOPE,
    callback: (resp) => {
      if (resp.error) {
        pendingConvert = false;
        setStatus("Sign-in failed: " + resp.error, true);
        return;
      }
      accessToken = resp.access_token;
      tokenExpiresAt = Date.now() + (Number(resp.expires_in) - 60) * 1000; // 60 s margin
      updateButtons();
      fetchUserEmail();
      if (pendingConvert) {
        pendingConvert = false;
        runConvert();
      } else {
        setStatus("Signed in. Pick an SVG and a presentation, then convert.");
      }
    },
  });
}

loginBtn.addEventListener("click", () => {
  initTokenClient();
  if (accessToken) {
    // Already signed in: let the user pick another account.
    userEmail = null;
    tokenClient.requestAccessToken({ prompt: "select_account" });
  } else {
    tokenClient.requestAccessToken(); // consent popup on first grant
  }
});

// Silent re-grant: no popup if the Google session is still active.
function refreshToken() {
  if (!tokenClient) initTokenClient();
  tokenClient.requestAccessToken({ prompt: "" });
}

// ---- Convert ----------------------------------------------------------------

convertBtn.addEventListener("click", () => {
  if (Date.now() >= tokenExpiresAt) {
    pendingConvert = true;
    setStatus("Access token expired — refreshing…");
    refreshToken();
    return;
  }
  runConvert();
});

async function runConvert() {
  warningsEl.textContent = "";
  if (!selectedFile) {
    setStatus("Please choose an SVG file first.", true);
    return;
  }
  if (!presUrlInput.value.trim()) {
    setStatus("Please paste the target presentation URL (or ID).", true);
    return;
  }

  convertBtn.disabled = true;
  setStatus("Converting " + selectedFile.name + "…");
  try {
    const svgText = await selectedFile.text();
    const res = await svg2gslideConvert(svgText, presUrlInput.value, accessToken, "");
    const link = document.createElement("a");
    link.href = res.slideUrl;
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = "Open the new slide";
    setStatus("");
    statusEl.append(
      `Slide ${res.slideId} created (${res.requestCount} requests, phase "${res.phase}"). `,
      link
    );
    if (res.warnings.length) {
      warningsEl.textContent =
        "Approximations:\n" + res.warnings.map((w) => "  • " + w).join("\n");
    }
  } catch (err) {
    if (err && err.status === 401) {
      // Token revoked or expired server-side: get a fresh one and retry once.
      accessToken = null;
      pendingConvert = true;
      setStatus("Session expired — signing in again…");
      refreshToken();
    } else {
      setStatus("Error: " + (err && err.message ? err.message : err), true);
    }
  } finally {
    updateButtons();
  }
}

// ---- Init -------------------------------------------------------------------

clientIdInput.value = localStorage.getItem(CLIENT_ID_KEY) || DEFAULT_CLIENT_ID;
clientIdInput.addEventListener("input", () => {
  // The client ID is a public identifier (not a secret) — safe to persist.
  localStorage.setItem(CLIENT_ID_KEY, clientIdInput.value.trim());
  updateButtons();
});
updateButtons();
