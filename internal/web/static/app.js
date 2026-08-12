(() => {
  "use strict";

  const $ = (selector) => document.querySelector(selector);
  const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
	const list = (value) => Array.isArray(value) ? value : [];
	const number = (value, fallback = 0) => Number.isFinite(Number(value)) ? Number(value) : fallback;
	const precise = (value) => Number.isFinite(Number(value)) ? Number(value).toPrecision(5) : "-";
  const fmtDate = (value) => {
    const parsed = new Date(value);
    return Number.isNaN(parsed.valueOf()) ? "-" : parsed.toLocaleString("zh-CN", { hour12: false });
  };
  const empty = (message) => `<div class="empty">${esc(message)}</div>`;
  const phaseNames = { plan: "计划", recovery: "恢复", delivery: "交付物", verification: "验证", infrastructure: "基础设施", observation: "观测", internal: "内部" };
  const summaryText = (value) => {
    const text = String(value || "");
    const exact = {
      "check passed": "检查通过",
      "no unexpected infrastructure drift": "未发现意外基础设施漂移",
      "release candidate is ready": "发布候选已就绪",
      "ReleaseGuard target commit matches a clean DevCycle source": "ReleaseGuard 目标提交与干净的 DevCycle 代码来源一致",
      "metric baseline is missing": "缺少指标基线",
    };
    if (exact[text]) return exact[text];
    let match = text.match(/^(\d+) unexpected infrastructure changes$/);
    if (match) return `发现 ${match[1]} 项意外基础设施变化`;
    match = text.match(/^(\d+) commits and (\d+) changed files captured$/);
    if (match) return `已记录 ${match[1]} 个提交和 ${match[2]} 个变更文件`;
    match = text.match(/^all (\d+) nodes report the expected version$/);
    if (match) return `${match[1]} 个节点均上报预期版本`;
    match = text.match(/^(\d+)-second observation window passed with (\d+) samples$/);
    if (match) return `${match[1]} 秒观察窗口通过，共 ${match[2]} 个样本`;
    return text;
  };
  const decisionReasonText = (value) => {
    const text = String(value || "");
    const exact = {
      "all required release checks passed; final approval remains a human decision": "全部必须门禁已通过；最终发布仍需人工决策。",
      "post-release observation is in progress": "发布后观察正在进行。",
      "release validation checks are in progress": "发布验证正在进行。",
      "validation interrupted; rerun the same plan to restart deterministic checks": "验证被中断；请使用同一计划重新运行确定性门禁。",
      "observation interrupted; resume the persisted run": "观察被中断；请恢复已持久化的运行。",
    };
    if (exact[text]) return exact[text];
    let match = text.match(/^(\d+) optional checks need review$/);
    if (match) return `${match[1]} 个可选门禁需要审核。`;
    match = text.match(/^run abandoned:\s*(.*)$/);
    if (match) return `运行已放弃：${match[1]}`;
    const split = text.indexOf(": ");
    if (split > 0) return `${text.slice(0, split)}：${summaryText(text.slice(split + 2))}`;
    return summaryText(text);
  };
  const stageNames = { checking: "检查中", observing: "观察中", finalizing: "报告落盘", completed: "已完成", abandoned: "已放弃" };
  let report;
  let capabilities = { approval_write: false };
	let liveTimer;
	let capabilityWarningShown = false;

  async function get(path) {
    const response = await fetch(path, { cache: "no-store" });
    if (!response.ok) {
      const error = new Error((await response.text()).trim() || `HTTP ${response.status}`);
      error.status = response.status;
      throw error;
    }
    return response.json();
  }

  async function post(path, body, token) {
    const response = await fetch(path, {
      method: "POST",
      cache: "no-store",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      const error = new Error((await response.text()).trim() || `HTTP ${response.status}`);
      error.status = response.status;
      throw error;
    }
    return response.json();
  }

  function notify(message, kind = "error") {
    const target = $("#error");
	$("#error-message").textContent = message;
    target.className = `${kind} show`;
    target.setAttribute("role", kind === "error" ? "alert" : "status");
  }

  function renderCandidate(candidate) {
    if (!candidate) {
      $("#candidate").innerHTML = empty("计划未提供 DevCycle 发布候选，其他证据仍会照常检查。");
      return;
    }
    $("#candidate").innerHTML = `<dl class="kv"><dt>需求</dt><dd>${esc(candidate.requirement_title || candidate.requirement_id)}</dd><dt>验收标准</dt><dd>${number(candidate.criteria_satisfied)}/${number(candidate.criteria_total)}</dd><dt>有证据标准</dt><dd>${number(candidate.criteria_with_evidence)}/${number(candidate.criteria_total)}</dd><dt>开发任务</dt><dd>${number(candidate.tasks_done)}/${number(candidate.tasks_total)}</dd><dt>代码来源</dt><dd>${number(candidate.sources_clean)}/${number(candidate.sources_total)} 干净且钉住</dd><dt>候选状态</dt><dd><span class="state ${candidate.ready ? "pass" : "fail"}">${candidate.ready ? "已就绪" : "未就绪"}</span></dd></dl>`;
  }

  function renderChecks() {
    const filter = $("#check-filter").value;
	const indexed = list(report.results).map((result) => ({ result }));
    const rows = indexed.filter(({ result }) => !filter || result.status === filter);
    const failedRequired = indexed.filter(({ result }) => result.required && result.status === "fail").length;
    const totalDuration = indexed.reduce((total, { result }) => total + Number(result.duration_ms || 0), 0);
    const passed = indexed.filter(({ result }) => result.status === "pass").length;
    $("#check-summary").innerHTML = `<span><b>${passed}</b> 通过</span><span class="${failedRequired ? "fail" : ""}"><b>${failedRequired}</b> 必须门禁失败</span><span><b>${totalDuration} ms</b> 总耗时</span>`;
	$("#check-list").innerHTML = rows.length ? `<table><thead><tr><th>状态</th><th>检查</th><th>阶段</th><th>类型</th><th>耗时</th><th>要求</th><th>证据</th></tr></thead><tbody>${rows.map(({ result }) => `<tr class="${result.status === "fail" ? "failed-row" : ""}"><td><span class="state ${esc(result.status)}">${result.status === "pass" ? "通过" : "失败"}</span></td><td><span class="check-name">${esc(result.name)}</span><span class="sub">${esc(summaryText(result.summary))}</span></td><td><span class="phase">${esc(phaseNames[result.phase] || result.phase || "验证")}</span></td><td><span class="pill">${esc(result.type)}</span></td><td>${number(result.duration_ms)} ms</td><td>${result.required ? "必须" : "可选"}</td><td><button class="evidence-btn" data-evidence="${esc(result.name)}" type="button">查看证据</button></td></tr>`).join("")}</tbody></table>` : empty("没有符合筛选条件的检查。");
  }

  function renderGit(git) {
    if (!git) {
      $("#git-range").textContent = "未配置 Git 范围";
      $("#git-stats").innerHTML = "";
      $("#commits").innerHTML = $("#sensitive").innerHTML = $("#files").innerHTML = empty("计划未配置 Git 范围。");
      return;
    }
	const commits = list(git.commits); const changedFiles = list(git.changed_files); const migrations = list(git.migration_files); const configurations = list(git.configuration_files); const unexpected = list(git.unexpected_sensitive_files);
	$("#git-range").textContent = `${git.base_ref || "-"} → ${git.target_ref || "-"}`;
	$("#git-stats").innerHTML = [[commits.length, "提交"], [changedFiles.length, "文件"], [migrations.length, "迁移"], [git.working_tree_clean ? "干净" : `${list(git.dirty_files).length} 项`, "工作区"]].map(([value, label]) => `<div class="metric"><span>${label}</span><b>${esc(value)}</b></div>`).join("");
	$("#commits").innerHTML = commits.length ? commits.map((item) => `<div class="list-item">${esc(item)}</div>`).join("") : empty("没有提交");
	const sensitive = [...migrations.map((item) => [item, "数据库迁移"]), ...configurations.map((item) => [item, "配置变化"])];
	$("#sensitive").innerHTML = sensitive.length ? sensitive.map(([item, kind]) => `<div class="list-item ${unexpected.includes(item) ? "danger" : ""}"><b>${kind}</b><span class="sub">${esc(item)}</span></div>`).join("") : empty("未检测到迁移或配置文件");
	$("#files").innerHTML = changedFiles.length ? `<table><tbody>${changedFiles.map((item) => `<tr><td>${esc(item)}</td></tr>`).join("")}</tbody></table>` : empty("没有变化文件");
  }

  function renderFleet(fleet, source) {
    if (!fleet) {
      $("#fleet-source").textContent = "计划未配置 FleetScope 节点核验";
      $("#fleet-table").innerHTML = empty("没有目标节点证据。");
      return;
    }
    $("#fleet-source").innerHTML = `<span class="source-badge">${source === "after" ? "发布后观察" : "发布前基线"}</span> FleetScope 实际观测于 ${fmtDate(fleet.checked_at)}`;
	const nodes = list(fleet.nodes);
	$("#fleet-table").innerHTML = nodes.length ? `<table><thead><tr><th>节点</th><th>健康</th><th>期望版本</th><th>实际版本</th><th>结果</th><th>观察时间</th></tr></thead><tbody>${nodes.map((node) => `<tr class="${node.match ? "" : "failed-row"}"><td>${esc(node.node_id)}</td><td><span class="health ${esc(node.health)}">${esc(node.health)}</span></td><td>${esc(node.expected_version)}</td><td>${esc(node.actual_version || "未报告")}</td><td><span class="state ${node.match ? "pass" : "fail"}">${node.match ? "匹配" : "不匹配"}</span></td><td>${fmtDate(node.observed_at)}</td></tr>`).join("")}</tbody></table>` : empty("没有目标节点");
  }

  function renderObservation() {
    const check = (report.results || []).find((result) => result.type === "observation");
	const live = report.observation?.status === "observing";
	const liveSamples = list(report.observation?.samples);
    if (!check) {
	  if (live) {
		const deadline = new Date(report.observation.deadline_at).valueOf();
		const remaining = Number.isNaN(deadline) ? null : Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
		$("#observation").innerHTML = `<div class="observation-head"><span class="state pending">观察中</span><strong>${remaining === null ? "等待有效截止时间" : `剩余约 ${remaining} 秒`}，已保存 ${liveSamples.length} 个样本</strong></div>${renderSamples(liveSamples)}`;
		return;
	  }
	  $("#observation").innerHTML = empty("计划未配置发布后观察窗口。");
      return;
    }
    const samples = check.evidence?.samples || [];
	$("#observation").innerHTML = `<div class="observation-head"><span class="state ${esc(check.status)}">${check.status === "pass" ? "通过" : "失败"}</span><strong>${esc(check.summary)}</strong></div>${renderSamples(samples)}`;
  }

	function renderSamples(samples) {
		return list(samples).length ? `<div class="sample-strip">${list(samples).map((sample, index) => { const alerts = number(sample.critical_alerts); return `<div class="${alerts > 0 ? "danger" : ""}"><b>样本 ${index + 1}</b><span>${fmtDate(sample.checked_at)}</span><strong>${alerts}</strong><span>严重告警 · ${list(sample.nodes).length} 节点</span></div>`; }).join("")}</div>` : empty("等待第一个观察样本");
	}

	function renderMetricComparisons(metrics) {
		const target = $("#metric-comparisons");
		const comparisons = list(metrics);
		if (!comparisons.length) { target.innerHTML = empty("计划未配置指标回归策略。"); return; }
		target.innerHTML = `<table><thead><tr><th>指标</th><th>发布前</th><th>发布后</th><th>回归</th><th>结论</th></tr></thead><tbody>${comparisons.map((metric) => { const waiting=!metric.after; return `<tr class="${!waiting&&!metric.pass ? "failed-row" : ""}"><td><span class="check-name">${esc(metric.name)}</span><span class="sub">${esc(metric.metric)} · 时间 ${esc(metric.aggregate)} · 序列 ${esc(metric.series_reduce || "avg")}</span></td><td>${metric.before ? precise(metric.before.value) : "—"}</td><td>${metric.after ? precise(metric.after.value) : "采集中"}</td><td>${metric.after ? `${number(metric.regression_percent).toFixed(2)}%` : "—"}</td><td><span class="state ${waiting ? "pending" : metric.pass ? "pass" : "fail"}">${waiting ? "观察中" : metric.pass ? "通过" : "回归"}</span><span class="sub">${esc(metric.summary || "等待发布后窗口")}</span></td></tr>`; }).join("")}</tbody></table>`;
	}

  function renderEvidenceValue(value) {
    if (value === null || value === undefined || value === "") return '<span class="muted">无</span>';
    if (Array.isArray(value)) {
      if (!value.length) return '<span class="muted">空列表</span>';
      return `<ul class="evidence-list">${value.map((item) => `<li>${typeof item === "object" ? `<code>${esc(JSON.stringify(item))}</code>` : esc(item)}</li>`).join("")}</ul>`;
    }
    if (typeof value === "object") return `<pre class="evidence-object">${esc(JSON.stringify(value, null, 2))}</pre>`;
    if (typeof value === "string" && value.includes("\n")) return `<pre class="evidence-output">${esc(value)}</pre>`;
    return `<span>${esc(value)}</span>`;
  }

	function openEvidence(name) {
	const result = list(report?.results).find((item) => item.name === name);
    if (!result) {
      notify("这条检查证据已不存在，请刷新报告。");
      return;
    }
    const evidence = result.evidence || {};
    const entries = Object.entries(evidence);
    $("#evidence-title").textContent = result.name;
    $("#evidence-structured").innerHTML = `<div class="evidence-meta"><span class="state ${esc(result.status)}">${result.status === "pass" ? "通过" : "失败"}</span><span>${esc(phaseNames[result.phase] || result.phase || "验证")}</span><span>${esc(result.type)}</span><span>${esc(result.duration_ms)} ms</span></div><p>${esc(summaryText(result.summary))}</p>${entries.length ? `<dl>${entries.map(([key, value]) => `<dt>${esc(key)}</dt><dd>${renderEvidenceValue(value)}</dd>`).join("")}</dl>` : empty("该检查没有附加结构化证据。")}`;
    $("#evidence").textContent = JSON.stringify(evidence, null, 2);
    $("#evidence-dialog").showModal();
  }

  async function loadRuns() {
    const target = $("#run-history");
    if (!capabilities.live_runs) { target.innerHTML = empty("当前服务未连接运行数据库。"); return; }
    target.innerHTML = empty("正在读取运行历史…");
    try {
      const runs = await get("/api/v1/runs");
	  const history = list(runs);
	  target.innerHTML = history.length ? `<table><thead><tr><th>发布</th><th>阶段</th><th>决策</th><th>样本</th><th>更新时间</th><th>报告</th></tr></thead><tbody>${history.map((run) => `<tr><td><span class="check-name">${esc(run.release_id)}</span><span class="sub">${esc(run.id)}</span></td><td><span class="phase">${esc(stageNames[run.stage] || run.stage)}</span></td><td><span class="state ${esc(String(run.decision || "pending").toLowerCase())}">${esc(run.decision || "进行中")}</span></td><td>${number(run.samples)}</td><td>${fmtDate(run.updated_at)}</td><td>${run.decision ? `<button class="evidence-btn" data-run-report="${esc(run.id)}" type="button">查看</button>` : "—"}</td></tr>`).join("")}</tbody></table>` : empty("还没有运行记录。");
    } catch (error) {
      target.innerHTML = `<div class="approval-error"><b>运行历史读取失败</b><p>${esc(error.message)}</p></div>`;
    }
  }

  async function openRunReport(id) {
    try {
      const runReport = await get(`/api/v1/runs/${encodeURIComponent(id)}`);
      $("#evidence-title").textContent = `${runReport.release_id} · ${runReport.decision}`;
	  $("#evidence-structured").innerHTML = `<div class="evidence-meta"><span class="state ${esc(String(runReport.decision || "pending").toLowerCase())}">${esc(runReport.decision || "进行中")}</span><span>${fmtDate(runReport.generated_at)}</span><span>${list(runReport.results).length} 项门禁</span></div><p>${esc(decisionReasonText(runReport.decision_reason || ""))}</p>`;
      $("#evidence").textContent = JSON.stringify(runReport, null, 2);
      $("#evidence-dialog").showModal();
    } catch (error) { notify(`运行报告读取失败：${error.message}`); }
  }

  function setReportLock(approval) {
    if (approval) {
      $("#report-lock").classList.add("bound");
      $("#report-lock").innerHTML = `<span class="lock-icon" aria-hidden="true">✓</span><span><b>报告与人工决策已绑定</b><small>Approval SHA256 ${esc(approval.report_sha256)}</small></span>`;
      return;
    }
    $("#report-lock").classList.remove("bound");
    $("#report-lock").innerHTML = `<span class="lock-icon" aria-hidden="true">#</span><span><b>计划内容指纹已记录</b><small>Plan SHA256 ${esc(report.plan_sha256 || "未提供")}</small></span>`;
  }

  async function renderApproval() {
	if (report?.observation?.status === "observing") {
		$("#approval").innerHTML = `<div class="approval-state pending">观察尚未完成</div><p>运行样本正在持久化。只有最终不可变报告生成后才能写入人工决策。</p>`;
		setReportLock();
		return;
	}
    try {
      const approval = await get("/api/v1/approval");
	  $("#approval").innerHTML = `<div class="immutable-mark">不可变批准记录</div><div class="approval-state ${esc(String(approval.decision || "").toLowerCase())}">${esc(approval.decision)}</div><dl class="kv"><dt>批准人</dt><dd>${esc(approval.approved_by)}</dd><dt>批准时间</dt><dd>${fmtDate(approval.approved_at)}</dd><dt>报告 SHA256</dt><dd class="hash">${esc(approval.report_sha256)}</dd><dt>说明</dt><dd>${esc(approval.note || "-")}</dd></dl>`;
      setReportLock(approval);
    } catch (error) {
      setReportLock();
      if (error.status !== 404) {
        $("#approval").innerHTML = `<div class="approval-error"><b>批准记录读取失败</b><p>${esc(error.message)}</p><button type="button" data-retry-approval>重试</button></div>`;
        return;
      }
      const decisions = report.decision === "GO" ? ["GO", "HOLD", "NO-GO"] : report.decision === "HOLD" ? ["HOLD", "NO-GO"] : ["NO-GO"];
	  const readOnlyMessage = capabilities.error ? "无法确认服务器审批能力，已按只读模式保护。请恢复服务连接后重试。" : capabilities.approval_blocked_by_active_run ? "验证运行仍在进行，最终报告落盘前禁止批准。" : "当前报告服务为只读模式。运行 releaseguard confirm，或设置临时批准 Token 后启动服务。";
      $("#approval").innerHTML = capabilities.approval_write ? `<div class="approval-state pending">等待人工决策</div><form id="approval-form" class="approval-form"><label>决策<select name="decision">${decisions.map((value) => `<option value="${value}">${value}</option>`).join("")}</select></label><p class="approval-guard">人工决策只能维持或收紧自动门禁，不能把 ${esc(report.decision)} 放宽。</p><label>批准人<input name="approvedBy" required maxlength="100" autocomplete="name"></label><label>说明<textarea name="note" rows="3" maxlength="2000" placeholder="记录风险判断、例外和后续动作"></textarea></label><label>批准 Token<input name="token" type="password" minlength="24" required autocomplete="off"></label><label class="binding-check"><input name="binding" type="checkbox" required> <span>我确认决策将与当前报告 SHA256 绑定，写入后不可覆盖</span></label><button type="submit">写入不可变决策</button></form>` : `<div class="approval-state pending">尚未人工确认</div><p>${esc(readOnlyMessage)}</p>`;
    }
  }

  function render(data) {
	try {
    report = data;
    $("#release-label").textContent = `${data.release_id || "未命名发布"} · ${data.version || "未标记版本"}`;
    const decision = $("#decision");
    decision.textContent = data.decision || "UNKNOWN";
    decision.className = String(data.decision || "unknown").toLowerCase();
    $("#generated").textContent = `生成于 ${fmtDate(data.generated_at)}`;
	const results = list(data.results);
    const passed = results.filter((result) => result.status === "pass").length;
    const required = results.filter((result) => result.required).length;
	$("#metrics").innerHTML = [[data.version || "-", "版本"], [`${passed}/${results.length}`, "检查通过"], [required, "必须门禁"], [list(data.manifest?.git?.commits).length, "包含提交"]].map(([value, label]) => `<div class="metric"><span>${label}</span><b>${esc(value)}</b></div>`).join("");
    $("#reason").textContent = decisionReasonText(data.decision_reason) || (data.decision === "GO" ? "全部必须门禁已通过。" : "存在未通过的必须门禁，请进入门禁结果处理。");
    $("#reason").classList.toggle("fail", data.decision !== "GO");
    $("#decision-actions").innerHTML = data.decision === "GO" ? `<span class="eyebrow">建议动作</span><h3>完成人工批准</h3><p>自动门禁已通过。核对变更、观察窗口和回滚步骤后写入人工决策。</p><button type="button" data-jump="rollback">前往批准</button>` : `<span class="eyebrow">阻断动作</span><h3>处理失败门禁</h3><p>发布当前不可继续。先查看失败证据并修复，再生成一份新报告。</p><button type="button" data-jump="checks" data-set-filter="fail">查看失败项</button>`;
    renderCandidate(data.manifest?.candidate);
    $("#integrity").innerHTML = `<dl class="kv"><dt>报告 Schema</dt><dd>${esc(data.schema_version || "未提供")}</dd><dt>计划 SHA256</dt><dd class="hash">${esc(data.plan_sha256 || "未提供")}</dd><dt>清单生成时间</dt><dd>${fmtDate(data.manifest?.created_at)}</dd><dt>最终决策</dt><dd>独立批准文件</dd></dl>`;
    setReportLock();
    renderChecks();
    renderGit(data.manifest?.git);
    const fleetSource = data.manifest?.fleet_after ? "after" : data.manifest?.fleet_before ? "before" : "";
    renderFleet(data.manifest?.fleet_after || data.manifest?.fleet_before, fleetSource);
	renderMetricComparisons(data.manifest?.metrics || []);
    renderObservation();
	const rollback = list(data.rollback);
	$("#rollback-list").innerHTML = rollback.length ? rollback.map((step) => `<li>${esc(step)}</li>`).join("") : `<li>报告未提供回滚步骤，发布前必须补齐。</li>`;
    renderApproval();
    $("#export-json").disabled = false;
	clearTimeout(liveTimer);
	if (data.observation?.status === "observing" && !document.hidden) liveTimer = setTimeout(refreshLiveReport, 5000);
	} catch (error) {
		$("#fatal-message").textContent = `报告结构无法渲染：${error.message}`;
		$("#fatal").classList.add("show");
		$("#decision").textContent = "ERROR";
	}
  }

	async function refreshLiveReport() {
		if (document.hidden) return;
		try {
			const [nextReport, nextCapabilities] = await Promise.all([get("/api/v1/report"), get("/api/v1/capabilities")]);
			capabilities = nextCapabilities;
			if (nextReport.observation?.status === "observing") {
				report = nextReport;
				$("#generated").textContent = `更新于 ${fmtDate(nextReport.generated_at)}`;
				renderObservation();
				renderMetricComparisons(nextReport.manifest?.metrics || []);
				const source = nextReport.manifest?.fleet_after ? "after" : nextReport.manifest?.fleet_before ? "before" : "";
				renderFleet(nextReport.manifest?.fleet_after || nextReport.manifest?.fleet_before, source);
				liveTimer = setTimeout(refreshLiveReport, 5000);
			} else {
				render(nextReport);
			}
		} catch (error) { notify(`实时状态刷新失败：${error.message}`); liveTimer = setTimeout(refreshLiveReport, 5000); }
	}

  function navigate(target, updateHash = true) {
    const section = document.getElementById(target);
    if (!section?.classList.contains("view")) return;
	document.querySelectorAll("nav button[data-target]").forEach((item) => {
		const active = item.dataset.target === target;
		item.classList.toggle("active", active);
		if (active) item.setAttribute("aria-current", "page"); else item.removeAttribute("aria-current");
	});
    document.querySelectorAll(".view").forEach((item) => item.classList.toggle("active", item.id === target));
    if (updateHash) history.replaceState(null, "", `#/${target}`);
    if (target === "history") loadRuns();
	if (updateHash) section.focus({ preventScroll: true });
    window.scrollTo({ top: 0, behavior: "smooth" });
  }

  function routeFromHash() {
    navigate(location.hash.replace(/^#\/?/, "") || "summary", false);
  }

  async function submitApproval(form) {
    const button = form.querySelector('button[type="submit"]');
    const formData = new FormData(form);
    button.disabled = true;
    button.textContent = "正在绑定决策…";
    try {
      const approval = await post("/api/v1/approval", {
        decision: formData.get("decision"),
        approved_by: formData.get("approvedBy"),
        note: formData.get("note"),
      }, formData.get("token"));
      $("#approval").innerHTML = empty("批准记录已写入，正在重新读取。");
      setReportLock(approval);
      await renderApproval();
      notify("人工决策已写入，并与当前报告 SHA256 绑定。", "success");
    } catch (error) {
	  if (error.status === 409) {
		await renderApproval();
		notify("批准状态已经变化，已重新读取不可变记录。", "info");
		return;
	  }
      button.disabled = false;
      button.textContent = "写入不可变决策";
      notify(`批准失败：${error.message}`);
    }
  }

  async function load() {
    $("#fatal").classList.remove("show");
    $("#decision").textContent = "LOADING";
    const [reportResult, capabilityResult] = await Promise.allSettled([get("/api/v1/report"), get("/api/v1/capabilities")]);
	if (capabilityResult.status === "fulfilled") {
		capabilities = capabilityResult.value;
	} else {
		capabilities = { approval_write: false, error: true };
		if (!capabilityWarningShown) {
			capabilityWarningShown = true;
			notify("审批能力状态读取失败，页面已按只读模式保护。");
		}
	}
    if (reportResult.status === "rejected") {
      $("#fatal-message").textContent = reportResult.reason.message;
      $("#fatal").classList.add("show");
      $("#decision").textContent = "ERROR";
      return;
    }
    render(reportResult.value);
    routeFromHash();
  }

  document.addEventListener("click", (event) => {
    const navigation = event.target.closest("nav button[data-target], [data-jump]");
    if (navigation) {
      if (navigation.dataset.setFilter) {
        $("#check-filter").value = navigation.dataset.setFilter;
        renderChecks();
      }
      navigate(navigation.dataset.target || navigation.dataset.jump);
    }
    const evidenceButton = event.target.closest("[data-evidence]");
	if (evidenceButton) openEvidence(evidenceButton.dataset.evidence);
    const runButton = event.target.closest("[data-run-report]");
    if (runButton) openRunReport(runButton.dataset.runReport);
    if (event.target.closest("[data-retry-approval]")) renderApproval();
  });

  document.addEventListener("submit", (event) => {
    if (event.target.id !== "approval-form") return;
    event.preventDefault();
    submitApproval(event.target);
  });

  $("#close-dialog").addEventListener("click", () => $("#evidence-dialog").close());
	$("#dismiss-error").addEventListener("click", () => $("#error").classList.remove("show"));
  $("#check-filter").addEventListener("change", renderChecks);
  $("#refresh-runs").addEventListener("click", loadRuns);
  $("#retry-report").addEventListener("click", load);
  $("#print-report").addEventListener("click", () => window.print());
  $("#export-json").addEventListener("click", () => {
    if (!report) return;
    const blob = new Blob([JSON.stringify(report, null, 2)], { type: "application/json" });
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = `${report.release_id || "release"}-report.json`;
    link.click();
    URL.revokeObjectURL(link.href);
  });
  window.addEventListener("hashchange", routeFromHash);
	document.addEventListener("visibilitychange", () => {
		clearTimeout(liveTimer);
		if (!document.hidden && report?.observation?.status === "observing") refreshLiveReport();
	});
  load();
})();
