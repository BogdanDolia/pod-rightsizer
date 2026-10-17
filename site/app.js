"use strict";

const cpuBuffer = document.getElementById("cpu-buffer");
const memoryBuffer = document.getElementById("memory-buffer");
const memoryMultiplier = document.getElementById("memory-multiplier");
const patchOutput = document.getElementById("patch-output");
const copyStatus = document.getElementById("copy-status");

function renderPolicy() {
  const cpuPercent = Number(cpuBuffer.value);
  const memoryPercent = Number(memoryBuffer.value);
  const multiplier = Number(memoryMultiplier.value);
  const cpuRequest = 180 * (1 + cpuPercent / 100);
  const memoryRequest = 145 * (1 + memoryPercent / 100);
  // Round each exported quantity up, as the resource patch does.
  const rounded = (value) => Math.ceil(Number(value.toFixed(8)));
  document.getElementById("cpu-buffer-value").value = `${cpuPercent}%`;
  document.getElementById("memory-buffer-value").value = `${memoryPercent}%`;
  document.getElementById("memory-multiplier-value").value = `${multiplier.toFixed(1)}×`;
  patchOutput.textContent = `apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: demo
  name: checkout-api
spec:
  template:
    spec:
      containers:
      - name: api
        resources:
          requests:
            cpu: "${rounded(cpuRequest)}m"
            memory: "${rounded(memoryRequest)}Mi"
          limits:
            cpu: null
            memory: "${rounded(memoryRequest * multiplier)}Mi"`;
  copyStatus.textContent = "";
}

for (const control of [cpuBuffer, memoryBuffer, memoryMultiplier]) {
  control.addEventListener("input", renderPolicy);
}

document.getElementById("reset-policy").addEventListener("click", () => {
  cpuBuffer.value = "10";
  memoryBuffer.value = "20";
  memoryMultiplier.value = "1.2";
  renderPolicy();
});

for (const button of document.querySelectorAll("[data-copy]")) {
  button.addEventListener("click", async () => {
    const target = document.getElementById(button.dataset.copy);
    try {
      await navigator.clipboard.writeText(target.textContent);
      copyStatus.textContent = "Copied to clipboard.";
      const label = button.textContent;
      button.textContent = "Copied";
      window.setTimeout(() => { button.textContent = label; }, 1600);
    } catch (_) {
      copyStatus.textContent = "Select and copy the code manually; clipboard access is unavailable.";
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(target);
      selection.removeAllRanges();
      selection.addRange(range);
    }
  });
}

renderPolicy();
