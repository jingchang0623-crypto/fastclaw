"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslations } from "next-intl";
import { QRCodeSVG } from "qrcode.react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Bot,
  CheckCircle2,
  Loader2,
  MessageSquareText,
  QrCode,
  RefreshCw,
  Users,
} from "lucide-react";
import {
  getAgents,
  getAgentSystemFile,
  getChatHistory,
  getChatSessions,
  listAgentChannels,
  startAgentWeChatLogin,
  pollAgentWeChatLoginStatus,
  type AgentDetail,
  type ChatHistoryMessage,
} from "@/lib/api";

// "My Team" dashboard: one card per agent ("digital employee"). The
// boss-facing mental model is a staff roster, not an agent console —
// each card shows who the employee is (avatar + role + one-line duty
// from IDENTITY.md), whether they're reachable (WeChat binding), and
// what they've done lately (latest report + onboarding-interview
// state). Unbound employees surface an inline WeChat login QR — the
// single highest-leverage action for retention (audit: 79% of unbound
// users churned unreachable), which is why binding lives here on the
// landing page instead of buried in per-agent channel settings.

interface TeamMember {
  agent: AgentDetail;
  duty: string; // one-liner from IDENTITY.md (fallback: description)
  interviewed: boolean; // USER.md non-empty = onboarding interview has output
  wechatBound: boolean;
  latestReport: string; // last assistant message text, "" when none
  reportedToday: boolean;
  detailsLoaded: boolean;
}

// extractDuty pulls the one-line role blurb out of IDENTITY.md. The
// rolepack convention is a `- **一句话**：…` field; older/hand-written
// files may only have 头衔/职责 or free text, so fall through to the
// first meaningful line.
function extractDuty(md: string): string {
  // Checked in priority order — an alternation regex would return
  // whichever field appears first in the file (头衔 precedes 一句话 in
  // the rolepack layout), not the most duty-like one.
  for (const key of ["一句话", "职责", "头衔"]) {
    const m = md.match(new RegExp(`\\*\\*${key}\\*\\*\\s*[:：]\\s*(.+)`));
    if (m) return m[1].trim();
  }
  for (const raw of md.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    // Skip name/avatar bookkeeping fields — they duplicate the card
    // header or are instructions to the provisioner, not a duty.
    if (/\*\*(?:名字|姓名|头像建议?)\*\*/.test(line)) continue;
    const text = line
      .replace(/^[-*>]\s*/, "")
      .replace(/\*\*(.+?)\*\*/g, "$1")
      .trim();
    if (text) return text;
  }
  return "";
}

// stripMarkdown flattens a report message into plain text for the
// one-line summary slot.
function stripMarkdown(s: string): string {
  return s
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/[#>*_`~-]+/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function latestAssistantText(history: ChatHistoryMessage[]): string {
  for (let i = history.length - 1; i >= 0; i--) {
    const m = history[i];
    if (m.role === "assistant" && m.content && m.content.trim()) {
      return stripMarkdown(m.content);
    }
  }
  return "";
}

function isToday(unixMs: number): boolean {
  const d = new Date(unixMs);
  const now = new Date();
  return (
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  );
}

// broadcastChannelsChanged lets the sidebar's unbound-count red dot
// refresh the moment a QR scan lands, without a page reload.
function broadcastChannelsChanged() {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent("fastclaw:channels-changed"));
  }
}

export default function TeamPage() {
  const t = useTranslations("team");
  const [members, setMembers] = useState<TeamMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // Which card currently shows its inline QR. Single-flight: only one
  // QR polls at a time so N unbound employees don't fan out N pollers.
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const autoExpanded = useRef(false);

  const loadMemberDetails = useCallback(
    async (agent: AgentDetail): Promise<TeamMember> => {
      const [identity, userFile, channels, sessions] = await Promise.all([
        getAgentSystemFile(agent.id, "IDENTITY.md").catch(() => ({ content: "" })),
        getAgentSystemFile(agent.id, "USER.md").catch(() => ({ content: "" })),
        listAgentChannels(agent.id).catch(() => []),
        getChatSessions(agent.id).catch(() => []),
      ]);
      const wechatBound = channels.some((c) => c.type === "wechat");
      // Latest activity = session with max updatedAt; its last assistant
      // message is the freshest "report". History rows carry no per-
      // message timestamp, so "reported today" approximates via the
      // session's updatedAt — good enough for a status badge.
      let latestReport = "";
      let reportedToday = false;
      const latest = sessions.reduce(
        (best, s) =>
          (s.updatedAt || 0) > (best?.updatedAt || 0) ? s : best,
        undefined as (typeof sessions)[number] | undefined,
      );
      if (latest) {
        const history = await getChatHistory(agent.id, latest.id).catch(() => []);
        latestReport = latestAssistantText(history);
        reportedToday = !!latestReport && !!latest.updatedAt && isToday(latest.updatedAt);
      }
      return {
        agent,
        duty: extractDuty(identity.content) || agent.description || "",
        interviewed: userFile.content.trim().length > 0,
        wechatBound,
        latestReport,
        reportedToday,
        detailsLoaded: true,
      };
    },
    [],
  );

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const agents = await getAgents();
        if (cancelled) return;
        // Render skeleton-ish cards immediately, hydrate per-agent
        // details as they arrive.
        setMembers(
          agents.map((agent) => ({
            agent,
            duty: agent.description || "",
            interviewed: false,
            wechatBound: false,
            latestReport: "",
            reportedToday: false,
            detailsLoaded: false,
          })),
        );
        setLoading(false);
        const detailed = await Promise.all(agents.map(loadMemberDetails));
        if (cancelled) return;
        setMembers(detailed);
        // Onboarding default: open the first unbound employee's QR so a
        // fresh account lands on "scan this" instead of a wall of cards.
        if (!autoExpanded.current) {
          autoExpanded.current = true;
          const firstUnbound = detailed.find((m) => !m.wechatBound);
          if (firstUnbound) setExpandedId(firstUnbound.agent.id);
        }
      } catch (e) {
        if (!cancelled) {
          setError(e instanceof Error ? e.message : t("errorLoad"));
          setLoading(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [loadMemberDetails, t]);

  const handleBound = useCallback((agentId: string) => {
    setMembers((prev) =>
      prev.map((m) =>
        m.agent.id === agentId ? { ...m, wechatBound: true } : m,
      ),
    );
    broadcastChannelsChanged();
  }, []);

  const unboundCount = useMemo(
    () => members.filter((m) => m.detailsLoaded && !m.wechatBound).length,
    [members],
  );

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div>
        <div className="flex items-center gap-2">
          <Users className="size-5 text-muted-foreground" />
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
        </div>
        <p className="text-sm text-muted-foreground mt-1">{t("subtitle")}</p>
      </div>

      {unboundCount > 0 && (
        <div className="rounded-lg border border-amber-500/40 bg-amber-500/5 px-4 py-3 flex items-center gap-2">
          <span className="relative flex h-2 w-2 shrink-0">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-red-500 opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-red-500" />
          </span>
          <p className="text-sm text-amber-700 dark:text-amber-400">
            {t("unboundBanner", { count: unboundCount })}
          </p>
        </div>
      )}

      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4">
          <p className="text-sm text-destructive">{error}</p>
        </div>
      )}

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2">
          <Skeleton className="h-52" />
          <Skeleton className="h-52" />
        </div>
      ) : members.length === 0 && !error ? (
        <div className="rounded-lg border border-dashed p-10 text-center space-y-2">
          <Bot className="h-8 w-8 mx-auto text-muted-foreground/60" />
          <p className="text-sm text-muted-foreground">{t("noAgents")}</p>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {members.map((m) => (
            <MemberCard
              key={m.agent.id}
              member={m}
              qrOpen={expandedId === m.agent.id}
              onToggleQr={() =>
                setExpandedId((cur) => (cur === m.agent.id ? null : m.agent.id))
              }
              onBound={() => handleBound(m.agent.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function MemberAvatar({ agent }: { agent: AgentDetail }) {
  const [failed, setFailed] = useState(false);
  if (!agent.avatarUrl || failed) {
    return (
      <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-muted">
        <Bot className="h-6 w-6 text-muted-foreground" />
      </div>
    );
  }
  return (
    <img
      src={agent.avatarUrl}
      alt={agent.name || agent.id}
      className="h-12 w-12 shrink-0 rounded-xl object-cover"
      onError={() => setFailed(true)}
    />
  );
}

function StatusBadge({ member }: { member: TeamMember }) {
  const t = useTranslations("team");
  if (!member.detailsLoaded) {
    return <Skeleton className="h-5 w-16 rounded-full" />;
  }
  if (!member.wechatBound) {
    return (
      <span className="inline-flex items-center gap-1.5 rounded-full bg-red-500/10 px-2.5 py-0.5 text-xs font-medium text-red-600 dark:text-red-400">
        <span className="h-1.5 w-1.5 rounded-full bg-red-500" />
        {t("badgeUnbound")}
      </span>
    );
  }
  if (member.reportedToday) {
    return (
      <span className="inline-flex items-center gap-1.5 rounded-full bg-emerald-500/10 px-2.5 py-0.5 text-xs font-medium text-emerald-600 dark:text-emerald-400">
        <CheckCircle2 className="h-3 w-3" />
        {t("badgeReportedToday")}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full bg-sky-500/10 px-2.5 py-0.5 text-xs font-medium text-sky-600 dark:text-sky-400">
      <span className="h-1.5 w-1.5 rounded-full bg-sky-500" />
      {t("badgeBound")}
    </span>
  );
}

function MemberCard({
  member,
  qrOpen,
  onToggleQr,
  onBound,
}: {
  member: TeamMember;
  qrOpen: boolean;
  onToggleQr: () => void;
  onBound: () => void;
}) {
  const t = useTranslations("team");
  const { agent } = member;

  return (
    <div className="rounded-lg border border-border bg-card p-5 flex flex-col gap-4">
      <div className="flex items-start gap-3">
        <MemberAvatar agent={agent} />
        <div className="flex-1 min-w-0 space-y-1">
          <div className="flex items-center justify-between gap-2">
            <span className="font-semibold truncate">
              {agent.name || agent.id}
            </span>
            <StatusBadge member={member} />
          </div>
          <p className="text-sm text-muted-foreground line-clamp-2">
            {member.duty || t("noDuty")}
          </p>
        </div>
      </div>

      {member.wechatBound ? (
        <BoundBody member={member} />
      ) : member.detailsLoaded ? (
        <UnboundBody
          agent={agent}
          qrOpen={qrOpen}
          onToggleQr={onToggleQr}
          onBound={onBound}
        />
      ) : (
        <Skeleton className="h-16" />
      )}
    </div>
  );
}

function BoundBody({ member }: { member: TeamMember }) {
  const t = useTranslations("team");
  return (
    <div className="space-y-3">
      <div className="rounded-md bg-muted/40 p-3 space-y-1">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <MessageSquareText className="h-3.5 w-3.5" />
          {t("latestReport")}
        </div>
        {member.latestReport ? (
          <p className="text-sm line-clamp-3">{member.latestReport}</p>
        ) : (
          <p className="text-sm text-muted-foreground">{t("noReport")}</p>
        )}
      </div>
      <div className="flex items-center justify-between text-xs">
        <span className="text-muted-foreground">{t("interviewLabel")}</span>
        {member.interviewed ? (
          <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="h-3 w-3" />
            {t("interviewDone")}
          </span>
        ) : (
          <span className="text-amber-600 dark:text-amber-400">
            {t("interviewPending")}
          </span>
        )}
      </div>
    </div>
  );
}

// UnboundBody drives the inline WeChat QR login. Same state machine as
// the per-agent channels dialog (start → render QR → poll 3s →
// confirmed/expired), but embedded in the card so binding is zero-
// navigation from the landing page. Poll lifecycle is tied to qrOpen —
// collapsing the panel (or another card opening) stops the interval.
function UnboundBody({
  agent,
  qrOpen,
  onToggleQr,
  onBound,
}: {
  agent: AgentDetail;
  qrOpen: boolean;
  onToggleQr: () => void;
  onBound: () => void;
}) {
  const t = useTranslations("team");
  type WechatStatus = "wait" | "scaned" | "confirmed" | "expired" | "";
  const [qrPayload, setQrPayload] = useState("");
  const [status, setStatus] = useState<WechatStatus>("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  const startLogin = useCallback(async () => {
    setLoading(true);
    setError("");
    setStatus("");
    setQrPayload("");
    stopPolling();
    const res = await startAgentWeChatLogin(agent.id);
    setLoading(false);
    if (res.error || !res.sessionId || !res.qrCodeImg) {
      setError(res.error || t("errorQr"));
      return;
    }
    const sessionId = res.sessionId;
    setQrPayload(res.qrCodeImg);
    setStatus("wait");
    pollRef.current = setInterval(async () => {
      const s = await pollAgentWeChatLoginStatus(agent.id, sessionId);
      if (s.error) {
        // Transient iLink hiccups recover on the next tick — surface
        // without killing the loop.
        setError(s.error);
        return;
      }
      setError("");
      if (s.status) setStatus(s.status as WechatStatus);
      if (s.connected) {
        stopPolling();
        onBound();
      }
      if (s.status === "expired") {
        stopPolling();
      }
    }, 3000);
  }, [agent.id, onBound, stopPolling, t]);

  // Open → fetch QR; close/unmount → stop polling and reset so a
  // re-open gets a fresh code (the old token may have expired while
  // collapsed).
  useEffect(() => {
    if (qrOpen) {
      startLogin();
    } else {
      stopPolling();
      setQrPayload("");
      setStatus("");
      setError("");
      setLoading(false);
    }
    return stopPolling;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [qrOpen]);

  if (!qrOpen) {
    return (
      <Button onClick={onToggleQr} className="w-full">
        <QrCode className="h-4 w-4 mr-1.5" />
        {t("bindAction")}
      </Button>
    );
  }

  return (
    <div className="flex flex-col items-center gap-3 rounded-md border bg-muted/20 p-4">
      <p className="text-xs text-muted-foreground">
        {t("bindHint", { name: agent.name || agent.id })}
      </p>
      {loading ? (
        <div className="flex h-44 w-44 items-center justify-center">
          <Loader2 className="h-7 w-7 animate-spin text-muted-foreground" />
        </div>
      ) : qrPayload ? (
        <div className="rounded-lg border bg-white p-3">
          <QRCodeSVG value={qrPayload} size={168} level="M" />
        </div>
      ) : (
        <div className="flex h-44 w-44 items-center justify-center text-muted-foreground">
          <QrCode className="h-7 w-7 opacity-50" />
        </div>
      )}

      <div className="flex items-center gap-1.5 text-xs text-muted-foreground min-h-4">
        {status === "wait" && t("statusWait")}
        {status === "scaned" && (
          <>
            <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
            {t("statusScanned")}
          </>
        )}
        {status === "confirmed" && (
          <>
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            {t("binding")}
          </>
        )}
        {status === "expired" && (
          <span className="text-destructive">{t("statusExpired")}</span>
        )}
      </div>

      {error && <p className="text-xs text-destructive">{error}</p>}

      <div className="flex gap-2">
        {status === "expired" && (
          <Button size="sm" onClick={startLogin} disabled={loading}>
            <RefreshCw className="h-3.5 w-3.5 mr-1" />
            {t("refreshQr")}
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={onToggleQr}>
          {t("collapse")}
        </Button>
      </div>
    </div>
  );
}
