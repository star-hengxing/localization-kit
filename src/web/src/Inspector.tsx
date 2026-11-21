import { useEffect, useRef, useState } from "react";
import { unitTone } from "./CompareViewer";
import { entryKey, isCurrentReview, parseBreaks, readOverrides, revisionKey, staleLabel, STATUS_LABELS, TAGS } from "./feedback";
import type { FeedbackEntry, Overrides, ReviewFields, ReviewStatus } from "./feedback";
import { PAGE_UNIT, renderParameters } from "./manifest";
import type { Page, Unit } from "./manifest";

const UNIT_LABELS = { rendered: "已嵌字", skipped: "已跳过", failed: "失败" } as const;
const STATUSES: ReviewStatus[] = ["approved", "problem", "pending"];

function UnitDetails({ unit }: { unit: Unit }) {
  return <>
    <dl className="render-params">
      {renderParameters(unit).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
      {unit.protectedKind && <div><dt>保护类型</dt><dd>{unit.protectedKind}</dd></div>}
    </dl>
    {(unit.src || unit.dst || unit.columns.length > 0) && <dl className="unit-text">
      {unit.src && <div><dt>原文</dt><dd lang="ja">{unit.src}</dd></div>}
      {unit.dst && <div><dt>译文</dt><dd>{unit.dst}</dd></div>}
      {unit.columns.length > 0 && <div><dt>列划分</dt><dd><ol>{unit.columns.map((column, index) =>
        <li key={index}>{column}</li>)}</ol></dd></div>}
    </dl>}
    {unit.qa_issues.length > 0 && <ul className="qa-issues">
      {unit.qa_issues.map((issue, index) => <li key={index}>{issue}</li>)}
    </ul>}
  </>;
}

function Suggestions({ overrides, onChange }: { overrides: Overrides; onChange: (value: Overrides) => void }) {
  const [draft, setDraft] = useState<{ key: "stroke" | "breaks" | "dst"; value: string } | null>(null);
  function update(key: keyof Overrides, value: unknown) {
    onChange({ [key]: readOverrides({ [key]: value })[key] });
  }

  return <details className="suggestions">
    <summary>覆盖参数建议{Object.keys(overrides).length > 0 && `（${Object.keys(overrides).length}）`}</summary>
    <div className="suggestion-fields">
      <label>擦字模式
        <select value={overrides.erase ?? ""} onChange={(event) => update("erase", event.target.value)}>
          <option value="">—</option><option>auto</option><option>flat</option><option>telea</option><option>keep</option>
        </select>
      </label>
      <label>字重
        <select value={overrides.weight ?? ""} onChange={(event) => update("weight", event.target.value)}>
          <option value="">—</option><option>regular</option><option>medium</option><option>bold</option><option>heavy</option>
        </select>
      </label>
      <label>字号（px）
        <input type="number" min={1} value={overrides.font_size ?? ""}
          onChange={(event) => { if (event.target.validity.valid) update("font_size", event.target.valueAsNumber); }} />
      </label>
      <label>描边宽度（px）
        <input type="number" min={0} step="any" value={overrides.stroke_width ?? ""}
          onChange={(event) => { if (event.target.validity.valid) update("stroke_width", event.target.valueAsNumber); }} />
      </label>
      <label className="full-field">描边颜色
        <input type="text" placeholder="#RRGGBB 或 false（禁用）" pattern="(#[0-9a-fA-F]{6}|false)"
          value={draft?.key === "stroke" ? draft.value : overrides.stroke === false ? "false" : overrides.stroke ?? ""}
          onBlur={() => setDraft(null)}
          onChange={(event) => {
            setDraft({ key: "stroke", value: event.target.value });
            if (event.target.validity.valid) {
              update("stroke", event.target.value === "false" ? false : event.target.value);
            }
          }} />
      </label>
      <label className="full-field">手动分列（每行一列）
        <textarea rows={2} value={draft?.key === "breaks" ? draft.value : overrides.breaks?.join("\n") ?? ""}
          onBlur={() => setDraft(null)} onChange={(event) => {
            setDraft({ key: "breaks", value: event.target.value });
            update("breaks", parseBreaks(event.target.value));
          }} />
      </label>
      <label className="full-field">修改译文
        <textarea rows={2} value={draft?.key === "dst" ? draft.value : overrides.dst ?? ""}
          onBlur={() => setDraft(null)} onChange={(event) => {
            setDraft({ key: "dst", value: event.target.value });
            update("dst", event.target.value);
          }} />
      </label>
    </div>
  </details>;
}

export function Inspector({ page, selectedId, feedback, disabled, onSelect, onChange }: {
  page: Page;
  selectedId: string;
  feedback: ReadonlyMap<string, FeedbackEntry[]>;
  disabled: boolean;
  onSelect(id: string): void;
  onChange(fields: Partial<ReviewFields>): void;
}) {
  const list = useRef<HTMLDivElement>(null);
  const currentButton = useRef<HTMLButtonElement>(null);
  const entriesFor = (id: string) => feedback.get(entryKey(page.id, id)) ?? [];
  const currentFor = (id: string) => entriesFor(id).find((entry) => isCurrentReview(page, entry));
  const current = currentFor(selectedId);
  const history = entriesFor(selectedId).filter((entry) => !isCurrentReview(page, entry));
  const unit = page.units.find((item) => item.id === selectedId);
  const selectedTags = current?.tags ?? [];
  const reviewed = page.units.filter((item) => currentFor(item.id)).length;
  const pageReview = currentFor(PAGE_UNIT);
  const stalePageReview = !pageReview && entriesFor(PAGE_UNIT)[0];

  useEffect(() => {
    const button = currentButton.current;
    const holder = list.current;
    if (!button || !holder) return;
    const item = button.getBoundingClientRect();
    const bounds = holder.getBoundingClientRect();
    if (item.top < bounds.top) holder.scrollTop -= bounds.top - item.top;
    else if (item.bottom > bounds.bottom) holder.scrollTop += item.bottom - bounds.bottom;
  }, [page.id, selectedId]);

  return <aside className="inspector" aria-label="页面审阅">
    <div className="inspector-section">
      <div className="section-heading">
        <h2>单元</h2><span className={page.qa.pass ? "qa-pass" : "qa-warn"}>{page.qa.pass ? "QA 通过" : "QA 告警"}</span>
      </div>
      {page.qa.issues.length > 0 && <ul className="qa-issues">
        {page.qa.issues.map((issue, index) => <li key={index}>{issue}</li>)}
      </ul>}
      <p className="page-progress">本页单元反馈 <strong>{reviewed} / {page.units.length}</strong></p>
      <div className="unit-list" ref={list} aria-label="单元列表">
        <button type="button" className="unit-row" aria-current={selectedId === PAGE_UNIT}
          ref={selectedId === PAGE_UNIT ? currentButton : undefined} onClick={() => onSelect(PAGE_UNIT)}>
          <span className={`unit-dot ${pageReview ? "reviewed" : stalePageReview ? "warning" : ""}`} />
          <span>整页</span><small title={stalePageReview ? staleLabel(stalePageReview) : undefined}>
            {pageReview ? STATUS_LABELS[pageReview.status] : stalePageReview ? "待复核" : "__page__"}
          </small>
        </button>
        {page.units.map((item) => {
          const entry = currentFor(item.id);
          const stale = !entry && entriesFor(item.id)[0];
          return <button type="button" key={item.id} className="unit-row" aria-current={selectedId === item.id}
            ref={selectedId === item.id ? currentButton : undefined} onClick={() => onSelect(item.id)}>
            <span className={`unit-dot ${stale ? "warning" : unitTone(item, !!entry)}`} />
            <span title={item.id}>{item.id}</span>
            <small title={stale ? staleLabel(stale) : undefined}>
              {UNIT_LABELS[item.status]}{entry ? ` · ${STATUS_LABELS[entry.status]}` : stale ? " · 待复核" : ""}
            </small>
          </button>;
        })}
      </div>
      {unit && <div className="selection-details"><h3>{unit.id}</h3><UnitDetails unit={unit} /></div>}
    </div>
    <div className="inspector-section feedback-form">
      <div className="section-heading"><h2>反馈</h2><span>{unit ? unit.id : "整页"}</span></div>
      {history.map((entry) => <details key={revisionKey(entry)} className="review-history">
        <summary>{staleLabel(entry)}</summary>
        <p>历史审核状态：{STATUS_LABELS[entry.status]}{entry.tags.length > 0 && ` · ${entry.tags.join("、")}`}</p>
        {entry.note && <p>{entry.note}</p>}
        {entry.overrides && <pre>{JSON.stringify(entry.overrides, null, 2)}</pre>}
      </details>)}
      <fieldset className="feedback-fields" disabled={disabled}>
        <fieldset className="review-status">
          <legend>审核状态</legend>
          {STATUSES.map((status) => <button type="button" key={status}
            className={status} aria-pressed={current?.status === status}
            onClick={() => onChange({ status })}>{STATUS_LABELS[status]}</button>)}
        </fieldset>
        <fieldset>
          <legend>问题标签</legend>
          <div className="tag-list">
            {TAGS.map((tag) => <button key={tag} type="button" aria-pressed={selectedTags.includes(tag)}
              onClick={() => onChange({ tags: TAGS.filter((item) =>
                item === tag ? !selectedTags.includes(item) : selectedTags.includes(item)) })}>
              {tag}
            </button>)}
          </div>
        </fieldset>
        <label className="note-field">备注
          <textarea rows={3} value={current?.note ?? ""} onChange={(event) => onChange({ note: event.target.value })} />
        </label>
        {unit && !unit.protectedKind && <Suggestions key={unit.id} overrides={current?.overrides ?? {}}
          onChange={(overrides) => onChange({ overrides })} />}
      </fieldset>
    </div>
  </aside>;
}
