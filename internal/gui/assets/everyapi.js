// The standalone console configures one EveryAPI connection, without a provider editor or marketplace.
Object.assign(I18N.zh, { "EveryAPI API key": "EveryAPI API 密钥", "EveryAPI region": "EveryAPI 接入区域", "Global endpoint": "全球节点", "China endpoint": "中国加速节点", "Configured endpoint": "已配置的节点", "Leave blank to keep the saved key": "留空保留已保存的密钥", "Save and refresh models": "保存并刷新模型", "Configure your EveryAPI API key, then refresh the model list.": "填写 EveryAPI API 密钥，然后刷新模型列表。", "Select the models exposed by this gateway.": "选择此网关对外提供的模型。" });
Object.assign(I18N["zh-TW"], { "EveryAPI API key": "EveryAPI API 金鑰", "EveryAPI region": "EveryAPI 接入區域", "Global endpoint": "全球節點", "China endpoint": "中國加速節點", "Configured endpoint": "已設定的節點", "Leave blank to keep the saved key": "留空以保留已儲存的金鑰", "Save and refresh models": "儲存並更新模型", "Configure your EveryAPI API key, then refresh the model list.": "填寫 EveryAPI API 金鑰，然後更新模型清單。", "Select the models exposed by this gateway.": "選擇此閘道提供的模型。" });
Object.assign(I18N.ja, { "EveryAPI API key": "EveryAPI API キー", "EveryAPI region": "EveryAPI 接続先", "Global endpoint": "グローバル", "China endpoint": "中国向け", "Configured endpoint": "設定済みの接続先", "Leave blank to keep the saved key": "空欄の場合は保存済みのキーを保持", "Save and refresh models": "保存してモデルを更新", "Configure your EveryAPI API key, then refresh the model list.": "EveryAPI API キーを入力し、モデル一覧を更新してください。", "Select the models exposed by this gateway.": "このゲートウェイで提供するモデルを選択してください。" });
Object.assign(I18N.de, { "EveryAPI API key": "EveryAPI-API-Schlüssel", "EveryAPI region": "EveryAPI-Region", "Global endpoint": "Global", "China endpoint": "China", "Configured endpoint": "Konfigurierter Endpunkt", "Leave blank to keep the saved key": "Leer lassen, um den gespeicherten Schlüssel zu behalten", "Save and refresh models": "Speichern und Modelle aktualisieren", "Configure your EveryAPI API key, then refresh the model list.": "EveryAPI-API-Schlüssel eingeben und die Modellliste aktualisieren.", "Select the models exposed by this gateway.": "Die von diesem Gateway angebotenen Modelle auswählen." });

function renderEveryAPI() {
  const host = document.querySelector("#view-providers");
  if (!host || host.querySelector("#everyapiConfig")?.dataset.dirty === "true") return;
  for (const id of ["providers", "offProviders", "offHead", "excluded", "addProvider", "addSheet", "pvHead", "movable", "providerDiscovery"]) document.getElementById(id).hidden = true;
  host.classList.remove("loading");
  host.removeAttribute("aria-busy");
  host.querySelector(".everyapi-config")?.remove();
  const box = el("section", "everyapi-config");
  host.prepend(box);
  box.append(el("h2", "", "EveryAPI"));
  if (providers?.fileError) { box.append(el("p", "err", providers.fileError)); return; }
  if (!providers) { box.append(el("p", "", t("Loading…"))); return; }
  const p = providers.providers.find(p => p.id === "everyapi");
  const form = el("form");
  form.id = "everyapiConfig";
  const field = (label, id, control) => { const line = el("div", "everyapi-field"), name = el("label", "", t(label)); name.htmlFor = id; control.id = id; line.append(name, control); form.append(line); return control; };
  const key = field("EveryAPI API key", "everyapiAPIKey", el("input"));
  key.type = "password";
  key.autocomplete = "off";
  key.placeholder = p?.key.set ? t("Leave blank to keep the saved key") : "sk-…";
  key.required = !p?.key.set;
  const region = field("EveryAPI region", "everyapiRegion", el("select"));
  const root = p?.chat || (window.bootPrefs.everyapiBase || "https://api.everyapi.ai") + "/v1";
  const current = root === "https://api.everyapi.ai/v1" ? "global" : root === "https://api-cn.everyapi.ai/v1" ? "cn" : "";
  const regions = [["global", "Global endpoint"], ["cn", "China endpoint"]];
  if (!current) regions.unshift(["", "Configured endpoint"]);
  for (const [value, label] of regions) { const option = el("option", "", t(label)); option.value = value; region.append(option); }
  region.value = current;
  form.append(el("p", "sub", t("Configure your EveryAPI API key, then refresh the model list.")));
  const modelList = el("fieldset", "everyapi-models");
  modelList.append(el("legend", "", t("Models")), el("p", "sub", t("Select the models exposed by this gateway.")));
  for (const model of p?.models || []) {
    const label = el("label", "everyapi-model"), input = el("input");
    input.type = "checkbox"; input.name = "models"; input.value = model.id; input.checked = !!model.on;
    label.append(input, el("span", "", model.id)); modelList.append(label);
  }
  if (p?.models.length) form.append(modelList);
  const actions = el("div", "everyapi-actions"), save = el("button", "text primary", t("Save")), refresh = el("button", "text", t("Save and refresh models"));
  save.type = "submit"; refresh.type = "button";
  let refreshAfterSave = false;
  refresh.onclick = () => { refreshAfterSave = true; form.requestSubmit(); };
  actions.append(save, refresh); form.append(actions); box.append(form);
  form.oninput = () => { form.dataset.dirty = "true"; };
  form.onsubmit = async event => {
    event.preventDefault();
    save.disabled = refresh.disabled = true;
    try {
      const body = { key: key.value, region: region.value };
      if (p?.models.length) body.models = [...modelList.querySelectorAll("input:checked")].map(input => input.value);
      providers = await api("everyapi", body);
      key.value = "";
      if (refreshAfterSave) providers = await api("everyapi/models", {});
      form.dataset.dirty = "false";
      renderEveryAPI(); status(t("Saved"));
    } catch (error) { status(error.message, "err"); }
    finally { refreshAfterSave = false; save.disabled = refresh.disabled = false; }
  };
}
