// Assignment accounting is independent of process liveness and hook activity.
export function workLabel(member) {
  const work = member.work;
  if (!work) return "Work status unknown";
  const label = {standby: "Standby", waiting: "Waiting on registered triggers", needs_review: "Needs stage report", assigned: "Assigned work / follow-up"}[work.state] || "Work status unknown";
  return label + (work.unreported?.length ? ` (${work.unreported.length})` : "") + (work.nextAt ? " · Next observation: " + new Date(work.nextAt).toLocaleString() : "");
}
export function activityLabel(member) {
  const a = member.agent;
  if (!a || a.status === "exited" || a.alive === false) return "Process offline";
  return (a.alive === true ? "Process online" : "Process status unknown") + " · " + (a.activity ? "Last activity: " + a.activity : "Activity unknown") + (a.activityAt ? " at " + new Date(a.activityAt).toLocaleString() : "");
}
export function protocolLabel(member) {
  const p = member.agent?.protocol;
  if (!p) return "Protocol status unknown";
  return `Protocol ${p.version.slice(0, 8)}: ` + (p.ackedAt ? "reading acknowledged" : p.readAt ? "read; acknowledgment pending" : "update pending");
}
