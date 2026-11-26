import { useEffect, useMemo, useState } from 'react';
import { useRemoteCall } from '../../context/useRemoteCall';
import type { RemoteSkillSummary, RemoteSkillsListResult } from '../../types/generated';
import { SettingsSection } from './SettingsLayout';

export function SkillsSettings({ projectID }: { projectID: string | null }) {
  const { call } = useRemoteCall();
  const [skills, setSkills] = useState<RemoteSkillSummary[] | null>(null);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  const [agent, setAgent] = useState('');
  const [query, setQuery] = useState('');

  useEffect(() => {
    let active = true;
    setSkills(null);
    setError('');
    void call<RemoteSkillsListResult>('skills.list', projectID ? { projectID } : {})
      .then((result) => { if (active) setSkills(result.skills); })
      .catch((err) => { if (active) setError(err instanceof Error ? err.message : '读取技能失败'); });
    return () => { active = false; };
  }, [call, projectID, reload]);

  const agents = useMemo(() => [...new Set((skills ?? []).map((skill) => skill.agent))].filter(Boolean), [skills]);
  const selectedAgent = agents.includes(agent) ? agent : '';
  const visibleSkills = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const entries = new Map<string, { skill: RemoteSkillSummary; agents: Set<string>; disabled: boolean }>();
    for (const skill of skills ?? []) {
      if (selectedAgent && skill.agent !== selectedAgent) continue;
      if (needle && !`${skill.name} ${skill.description}`.toLowerCase().includes(needle)) continue;
      const identity = JSON.stringify([skill.scope, skill.name.toLowerCase(), skill.digest || skill.agent]);
      const existing = entries.get(identity);
      if (existing) {
        existing.agents.add(skill.agent);
        existing.disabled &&= skill.disabled;
      } else {
        entries.set(identity, { skill, agents: new Set([skill.agent]), disabled: skill.disabled });
      }
    }
    return [...entries.entries()];
  }, [query, selectedAgent, skills]);

  return (
    <SettingsSection title="已安装技能">
      <div data-settings-item="skills-list" className="settings-row flex flex-wrap items-center gap-2">
        <input type="search" aria-label="搜索技能" placeholder="搜索技能" className="settings-input min-w-0 flex-1" value={query} onChange={(event) => setQuery(event.target.value)} />
        <select aria-label="按 Agent 筛选技能" className="settings-input" value={selectedAgent} onChange={(event) => setAgent(event.target.value)}>
          <option value="">全部 Agent</option>
          {agents.map((item) => <option key={item} value={item}>{item}</option>)}
        </select>
      </div>
      {error ? (
        <div className="settings-row" role="alert">
          <p className="settings-row-error">{error}</p>
          <button type="button" className="secondary-button settings-inline-button" onClick={() => setReload((id) => id + 1)}>重试</button>
        </div>
      ) : !skills || visibleSkills.length === 0 ? (
        <p className="settings-row settings-row-desc" role="status">{skills ? '没有匹配的技能' : '正在读取技能…'}</p>
      ) : (
        <ul>
          {visibleSkills.map(([key, entry]) => (
            <li key={key} className="settings-row">
              <div className="settings-row-main min-w-0">
                <div className="settings-row-title break-words">{entry.skill.name}</div>
                {entry.skill.description && <p className="settings-row-desc break-words">{entry.skill.description}</p>}
                <p className="settings-row-desc">
                  {[...entry.agents].filter(Boolean).join(' / ')}
                  {entry.skill.scope === 'project' ? ' · 项目' : ''}
                  {entry.disabled ? ' · 已停用' : ''}
                </p>
              </div>
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  );
}
