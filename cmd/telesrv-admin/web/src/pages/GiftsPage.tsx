import { CheckCircle2, FileJson2, Gem, Loader2, Pause, Pencil, Play, Plus, RefreshCw, Search, ShieldCheck, Upload, X } from "lucide-react";
import lottie from "lottie-web/build/player/lottie_light_canvas";
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, Metric, PageFrame, QueryPanel } from "../components/ui";
import { useI18n } from "../i18n";
import { formatDate } from "../lib/format";
import type { CommandResult, OfficialStarGiftRow, StarGiftRow } from "../types";
import { GiftCollectiblesModal } from "./GiftCollectiblesModal";

type OfficialGiftCategory = "all" | "upgrade" | "craft" | "basic";

function officialGiftAttributeCount(gift: OfficialStarGiftRow) {
  return gift.model_count + gift.pattern_count + gift.backdrop_count;
}

function formatBytes(value: number | string) {
        const bytes = Number(value);
        if (bytes < 1024) return `${bytes} B`;
        if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
        return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function LottiePreview({ giftID, revision, compact = false }: { giftID: string; revision: number; compact?: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const animation = useRef<ReturnType<typeof lottie.loadAnimation> | null>(null);
  const [playing, setPlaying] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api.giftAnimation(giftID).then((data) => {
      if (cancelled || !host.current) return;
      animation.current?.destroy();
      animation.current = lottie.loadAnimation({
        container: host.current,
        renderer: "canvas",
        loop: true,
        autoplay: true,
        animationData: structuredClone(data)
      });
    }).catch((err) => setError(errorMessage(err)));
    return () => {
      cancelled = true;
      animation.current?.destroy();
      animation.current = null;
    };
  }, [giftID, revision]);

  function toggle() {
    if (!animation.current) return;
    if (playing) animation.current.pause();
    else animation.current.play();
    setPlaying(!playing);
  }

  return (
    <div className={`gift-animation-shell ${compact ? "compact" : ""}`}>
      <div className="gift-animation" ref={host}>{error && <span>{error}</span>}</div>
      <button className="gift-play" type="button" onClick={toggle} aria-label={playing ? "Pause" : "Play"}>
        {playing ? <Pause size={14} /> : <Play size={14} />}
      </button>
    </div>
  );
}

function OfficialLottiePreview({ sourceGiftID }: { sourceGiftID: string }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let cancelled = false;
    let player: ReturnType<typeof lottie.loadAnimation> | null = null;
    api.officialGiftAnimation(sourceGiftID).then((data) => {
      if (cancelled || !host.current) return;
      player = lottie.loadAnimation({ container: host.current, renderer: "canvas", loop: true, autoplay: true, animationData: structuredClone(data) });
    }).catch(() => undefined);
    return () => { cancelled = true; player?.destroy(); };
  }, [sourceGiftID]);
  return <div className="gift-animation-shell"><div className="gift-animation" ref={host} /></div>;
}

export function GiftsPage() {
  const { t } = useI18n();
  const [gifts, setGifts] = useState<StarGiftRow[]>([]);
  const [query, setQuery] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [collectibleGift, setCollectibleGift] = useState<StarGiftRow | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [importSource, setImportSource] = useState<"official" | "file">("official");
  const [officialGifts, setOfficialGifts] = useState<OfficialStarGiftRow[]>([]);
  const [officialQuery, setOfficialQuery] = useState("");
  const [officialCategory, setOfficialCategory] = useState<OfficialGiftCategory>("all");
  const [sourceGiftID, setSourceGiftID] = useState("");
  const [includeCollectible, setIncludeCollectible] = useState(true);
  const [upgradeStars, setUpgradeStars] = useState("0");
  const [supplyTotal, setSupplyTotal] = useState("0");
  const [slugPrefix, setSlugPrefix] = useState("");
  const [giftID, setGiftID] = useState("0");
  const [title, setTitle] = useState("");
  const [stars, setStars] = useState("50");
  const [convertStars, setConvertStars] = useState("50");
  const [sortOrder, setSortOrder] = useState("0");
  const [enabled, setEnabled] = useState(true);
  // Раунд 31: настройки выпуска (количество, «кем выпущено», лимит на
  // пользователя, birthday, премиум, дата разблокировки покупки).
  const [availabilityTotal, setAvailabilityTotal] = useState("0");
  const [availabilityRemains, setAvailabilityRemains] = useState("0");
  const [releasedByType, setReleasedByType] = useState<"" | "user" | "channel">("");
  const [releasedByID, setReleasedByID] = useState("");
  const [perUserTotal, setPerUserTotal] = useState("0");
  const [birthday, setBirthday] = useState(false);
  const [requirePremium, setRequirePremium] = useState(false);
  const [lockedUntilDate, setLockedUntilDate] = useState("");
  // Раунд 31: блок настроек вайтлиста (gift_whitelists).
  const [whitelistOpen, setWhitelistOpen] = useState(false);
  const [whitelistBuyers, setWhitelistBuyers] = useState("");
  const [whitelistHidden, setWhitelistHidden] = useState(false);
  const [whitelistNumbered, setWhitelistNumbered] = useState(false);
  const [whitelistDisallowUpgrades, setWhitelistDisallowUpgrades] = useState(false);
  const [whitelistDisallowResell, setWhitelistDisallowResell] = useState(false);
  const [whitelistApply, setWhitelistApply] = useState(true);
  // Раунд 32: настройки аукциона каталога (ревизия: auction/slug/период/старт).
  const [auctionEnabled, setAuctionEnabled] = useState(false);
  const [auctionSlug, setAuctionSlug] = useState("");
  const [auctionGiftsPerRound, setAuctionGiftsPerRound] = useState("0");
  const [auctionStartDate, setAuctionStartDate] = useState("");
  // Раунд 32: рабочая строка star_gift_auctions — правится из режима
  // редактирования существующего подарка; пустые поля = «не менять».
  const [auctionRoundDuration, setAuctionRoundDuration] = useState("");
  const [auctionRoundDurations, setAuctionRoundDurations] = useState("");
  const [auctionRaiseMinBid, setAuctionRaiseMinBid] = useState(false);
  const [auctionRoundExtends, setAuctionRoundExtends] = useState("");
  const [auctionMinBidAmount, setAuctionMinBidAmount] = useState("");
  const [auctionEndDate, setAuctionEndDate] = useState("");
  const [auctionStatus, setAuctionStatus] = useState("");
  // Раунд 32: режим «edit» — правка всех параметров существующего подарка без
  // загрузки новой анимации (замена файла по-прежнему через вкладку «Заменить»).
  const [editGift, setEditGift] = useState<StarGiftRow | null>(null);
  const [reason, setReason] = useState("");
  const [preview, setPreview] = useState<CommandResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [importError, setImportError] = useState("");

  async function load() {
    setError("");
    try {
      setGifts((await api.gifts()).Gifts ?? []);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  useEffect(() => { void load(); }, []);

  useEffect(() => {
    if (!importOpen || importSource !== "official" || officialGifts.length > 0) return;
    api.officialGifts().then((value) => setOfficialGifts(value.gifts ?? [])).catch((err) => setImportError(errorMessage(err)));
  }, [importOpen, importSource, officialGifts.length]);

  const selectedOfficial = useMemo(() => officialGifts.find((gift) => gift.source_gift_id === sourceGiftID) ?? null, [officialGifts, sourceGiftID]);
  const officialCategoryCounts = useMemo(() => ({
    all: officialGifts.length,
    upgrade: officialGifts.filter((gift) => gift.can_upgrade).length,
    craft: officialGifts.filter((gift) => gift.can_craft).length,
    basic: officialGifts.filter((gift) => !gift.can_upgrade).length
  }), [officialGifts]);
  const visibleOfficial = useMemo(() => {
    const normalized = officialQuery.trim().toLowerCase();
    return officialGifts.filter((gift) => {
      const categoryMatches = officialCategory === "all" ||
        (officialCategory === "upgrade" && gift.can_upgrade) ||
        (officialCategory === "craft" && gift.can_craft) ||
        (officialCategory === "basic" && !gift.can_upgrade);
      return categoryMatches && (!normalized || gift.source_gift_id.includes(normalized) || gift.title.toLowerCase().includes(normalized));
    });
  }, [officialGifts, officialQuery, officialCategory]);

  const visibleGifts = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return gifts;
    return gifts.filter((gift) =>
      String(gift.GiftID).includes(normalized) ||
      gift.Title.toLowerCase().includes(normalized) ||
      gift.SourceFormat.toLowerCase().includes(normalized)
    );
  }, [gifts, query]);

  // Раунд 31: настройки выпуска и вайтлиста — общий блок для обеих вкладок
  // (копия из снимка / загрузка своего файла). Дата разблокировки вводится
  // календарём и уходит на сервер unix-секундами. Раунд 32: старт аукциона
  // вводится с точностью до минуты (datetime-local).
  function lockedUntilUnix() {
    const value = lockedUntilDate.trim();
    if (!value) return 0;
    const parsed = Date.parse(value + "T00:00:00Z");
    return Number.isFinite(parsed) ? Math.floor(parsed / 1000) : 0;
  }

  function auctionStartUnix() {
    const value = auctionStartDate.trim();
    if (!value) return 0;
    const parsed = Date.parse(value.length > 10 ? value : value + "T00:00:00Z");
    return Number.isFinite(parsed) ? Math.floor(parsed / 1000) : 0;
  }

  function auctionEndUnix() {
    const value = auctionEndDate.trim();
    if (!value) return 0;
    const parsed = Date.parse(value.length > 10 ? value : value + "T00:00:00Z");
    return Number.isFinite(parsed) ? Math.floor(parsed / 1000) : 0;
  }

  function auctionSettings() {
    return {
      auction: auctionEnabled,
      auction_slug: auctionSlug.trim().toLowerCase(),
      gifts_per_round: Math.max(0, Math.floor(Number(auctionGiftsPerRound) || 0)),
      auction_start_date: auctionStartUnix()
    };
  }

  function issueSettings() {
    const buyers = whitelistBuyers.split(/[\s,;]+/).map((value) => value.trim()).filter(Boolean);
    return {
      availability_total: Math.max(0, Math.floor(Number(availabilityTotal) || 0)),
      availability_remains: Math.max(0, Math.floor(Number(availabilityRemains) || 0)),
      released_by_type: releasedByType,
      released_by_id: releasedByID.trim() ? String(BigInt(releasedByID.trim())) : "0",
      per_user_total: Math.max(0, Math.floor(Number(perUserTotal) || 0)),
      birthday,
      require_premium: requirePremium,
      locked_until_date: lockedUntilUnix(),
      ...auctionSettings(),
      whitelist: whitelistOpen ? {
        allowed_buyers: buyers,
        hidden: whitelistHidden,
        numbered: whitelistNumbered,
        disallow_upgrades: whitelistDisallowUpgrades,
        disallow_resell: whitelistDisallowResell,
        apply: whitelistApply
      } : null
    };
  }

  function resetIssueSettings() {
    setAvailabilityTotal("0"); setAvailabilityRemains("0");
    setReleasedByType(""); setReleasedByID(""); setPerUserTotal("0");
    setBirthday(false); setRequirePremium(false); setLockedUntilDate("");
    setWhitelistOpen(false); setWhitelistBuyers(""); setWhitelistHidden(false);
    setWhitelistNumbered(false); setWhitelistDisallowUpgrades(false);
    setWhitelistDisallowResell(false); setWhitelistApply(true);
    setAuctionEnabled(false); setAuctionSlug(""); setAuctionGiftsPerRound("0"); setAuctionStartDate("");
    setAuctionRoundDuration(""); setAuctionRoundDurations(""); setAuctionRaiseMinBid(false);
    setAuctionRoundExtends(""); setAuctionMinBidAmount(""); setAuctionEndDate(""); setAuctionStatus("");
  }

  function uploadForm(confirm: boolean, commandID = "") {
    if (!file) throw new Error(t("gifts.fileRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    const form = new FormData();
    form.set("metadata", JSON.stringify({
      command_id: commandID,
      reason: reason.trim(),
      confirm,
                        gift_id: giftID,
                        title: title.trim(),
                        stars,
                        convert_stars: convertStars,
      enabled,
      sort_order: Number(sortOrder),
      ...issueSettings()
    }));
    form.set("file", file, file.name);
    return form;
  }

  function officialPayload(confirm: boolean, commandID = "") {
    if (!sourceGiftID) throw new Error(t("gifts.officialRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    return {
      command_id: commandID, reason: reason.trim(), confirm,
                source_gift_id: sourceGiftID, gift_id: giftID, title: title.trim(),
                stars, convert_stars: convertStars, enabled, sort_order: Number(sortOrder),
                include_collectible: includeCollectible, upgrade_stars: upgradeStars,
      supply_total: Number(supplyTotal), slug_prefix: slugPrefix.trim().toLowerCase(),
      ...issueSettings()
    };
  }

  function chooseOfficial(gift: OfficialStarGiftRow) {
    setSourceGiftID(gift.source_gift_id);
    setTitle(gift.title || t("gifts.officialUnnamed", { id: gift.source_gift_id }));
    setStars(String(gift.stars));
    setConvertStars(String(gift.convert_stars));
    // Раунд 32: у аукционных подарков upgrade_stars=0 (бесплатное улучшение),
    // поэтому пул атрибутов включаем по can_upgrade ИЛИ has_attributes —
    // иначе импорт аукционного подарка остался бы без ревизии коллекции.
    setIncludeCollectible(gift.can_upgrade || gift.has_attributes);
                setUpgradeStars(gift.upgrade_stars);
                setSupplyTotal(String(gift.availability_total > 0 ? gift.availability_total : Math.max(gift.upgrade_variants, 1)));
    setSlugPrefix(`official-${gift.source_gift_id}`);
    // Раунд 31: предзаполнение настроек выпуска данными снимка, чтобы
    // нетронутая форма импортировала подарок с исходными характеристиками.
    setAvailabilityTotal(String(gift.availability_total > 0 ? gift.availability_total : 0));
    setAvailabilityRemains(String(gift.availability_total > 0 ? gift.availability_total : 0));
    setPerUserTotal(String(gift.per_user_total > 0 ? gift.per_user_total : 0));
    setBirthday(gift.birthday);
    setRequirePremium(gift.require_premium);
    setLockedUntilDate(gift.locked_until_date > 0 ? new Date(gift.locked_until_date * 1000).toISOString().slice(0, 10) : "");
    setReleasedByType(""); setReleasedByID("");
    // Раунд 32: предзаполнение аукционных настроек данными снимка.
    setAuctionEnabled(gift.auction);
    setAuctionSlug(gift.auction ? (gift.auction_slug || `official-${gift.source_gift_id}`) : "");
    setAuctionGiftsPerRound(String(gift.auction ? Math.max(gift.gifts_per_round, 1) : 0));
    setAuctionStartDate(gift.auction_start_date > 0 ? new Date(gift.auction_start_date * 1000).toISOString().slice(0, 16) : "");
    setPreview(null);
  }

  // Раунд 32: payload редактирования всех параметров существующего подарка
  // (без анимации). Отправляется целиком — форма всегда предзаполнена.
  function editPayload(confirm: boolean, commandID = "") {
    if (!editGift) throw new Error(t("gifts.officialRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    return {
      command_id: commandID, reason: reason.trim(), confirm,
      gift_id: editGift.GiftID, title: title.trim(),
      stars, convert_stars: convertStars, enabled, sort_order: Number(sortOrder),
      ...issueSettings()
    };
  }

  // Раунд 32: признак того, что рабочие настройки аукциона изменены —
  // тогда после основной команды уходит вторая (set-gift-auction-settings).
  // В режиме добавления (editGift = null) признаком считаются любые заполненные
  // поля или включённый режим повышения — настройки применяются к только что
  // созданному подарку сразу после импорта.
  function auctionRuntimeDirty() {
    if (!editGift) {
      return Boolean(auctionRoundDuration.trim() || auctionRoundDurations.trim() || auctionRoundExtends.trim() ||
        auctionMinBidAmount.trim() || auctionEndDate.trim() || auctionStatus.trim() || auctionRaiseMinBid);
    }
    return Boolean(auctionRoundDuration.trim() || auctionRoundDurations.trim() || auctionRoundExtends.trim() ||
      auctionMinBidAmount.trim() || auctionEndDate.trim() || auctionStatus.trim() ||
      auctionRaiseMinBid !== editGift.AuctionRaiseMinBid);
  }

  function auctionRuntimePayload(confirm: boolean, commandID = "", giftIDOverride?: string) {
    const owner = editGift?.GiftID ?? giftIDOverride;
    if (!owner) throw new Error(t("gifts.officialRequired"));
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    const payload: Record<string, unknown> = {
      command_id: commandID, reason: reason.trim(), confirm, gift_id: owner,
      raise_min_bid: auctionRaiseMinBid
    };
    if (auctionRoundDuration.trim()) payload.round_duration = Math.max(1, Math.floor(Number(auctionRoundDuration) || 1));
    if (auctionRoundDurations.trim()) payload.round_durations = auctionRoundDurations.trim();
    if (auctionRoundExtends.trim()) payload.round_extends = auctionRoundExtends.trim();
    if (auctionMinBidAmount.trim()) payload.min_bid_amount = String(BigInt(auctionMinBidAmount.trim()));
    if (auctionEndDate.trim()) payload.end_date = auctionEndUnix();
    if (auctionStatus.trim()) payload.status = auctionStatus.trim();
    return payload;
  }

  async function validateImport() {
    setBusy(true); setImportError(""); setPreview(null);
    try {
      setPreview(editGift
        ? await api.updateGiftSettings(editPayload(false))
        : importSource === "official" ? await api.importOfficialGift(officialPayload(false)) : await api.importGift(uploadForm(false)));
    } catch (err) {
      setImportError(errorMessage(err));
    } finally { setBusy(false); }
  }

  async function confirmImport() {
    if (!preview) return;
    setBusy(true); setImportError("");
    try {
      if (editGift) {
        await api.updateGiftSettings(editPayload(true, preview.command_id));
        if (auctionRuntimeDirty()) await api.setGiftAuctionSettings(auctionRuntimePayload(true));
      } else if (importSource === "official") {
        const result = await api.importOfficialGift(officialPayload(true, preview.command_id));
        // Раунд 33: рабочие настройки аукциона из вкладки добавления применяются
        // к только что созданному подарку (id — из ответа импорта).
        if (auctionRuntimeDirty()) {
          const created = String((result.details as Record<string, unknown> | undefined)?.gift_id ?? "");
          await api.setGiftAuctionSettings(auctionRuntimePayload(true, "", created));
        }
      } else {
        const result = await api.importGift(uploadForm(true, preview.command_id));
        if (auctionRuntimeDirty()) {
          const created = String((result.details as Record<string, unknown> | undefined)?.gift_id ?? "");
          await api.setGiftAuctionSettings(auctionRuntimePayload(true, "", created));
        }
      }
                setPreview(null); setFile(null); setGiftID("0"); setTitle(""); setSourceGiftID(""); setEditGift(null);
      await load();
      setImportOpen(false);
    } catch (err) {
      setImportError(errorMessage(err));
    } finally { setBusy(false); }
  }

  function startImport() {
        setGiftID("0"); setTitle(""); setStars("50"); setConvertStars("50"); setSortOrder("0");
    setEnabled(true); setReason(""); setFile(null); setPreview(null); setImportError("");
    setEditGift(null);
    resetIssueSettings();
    setImportSource("official"); setSourceGiftID(""); setOfficialQuery(""); setOfficialCategory("all"); setImportOpen(true);
  }

  function startRevision(gift: StarGiftRow) {
    setGiftID(gift.GiftID); setTitle(gift.Title); setStars(String(gift.Stars));
    setConvertStars(String(gift.ConvertStars)); setSortOrder(String(gift.SortOrder)); setEnabled(gift.Enabled);
    setReason(""); setFile(null); setPreview(null); setImportError("");
    setEditGift(null);
    resetIssueSettings();
    setImportSource("official"); setSourceGiftID(""); setOfficialQuery(""); setOfficialCategory("all"); setImportOpen(true);
  }

  // Раунд 32: редактирование всех параметров существующего подарка без новой
  // анимации — форма предзаполняется текущими значениями (ревизия, вайтлист,
  // аукцион каталога и рабочая строка star_gift_auctions).
  function startEdit(gift: StarGiftRow) {
    setEditGift(gift);
    setGiftID(gift.GiftID); setTitle(gift.Title); setStars(String(gift.Stars));
    setConvertStars(String(gift.ConvertStars)); setSortOrder(String(gift.SortOrder)); setEnabled(gift.Enabled);
    setAvailabilityTotal(String(gift.AvailabilityTotal));
    setAvailabilityRemains(String(gift.AvailabilityRemains));
    setPerUserTotal(String(gift.PerUserTotal));
    setBirthday(gift.Birthday); setRequirePremium(gift.RequirePremium);
    setLockedUntilDate(gift.LockedUntilDate > 0 ? new Date(gift.LockedUntilDate * 1000).toISOString().slice(0, 10) : "");
    setReleasedByType(gift.ReleasedByType === "user" || gift.ReleasedByType === "channel" ? gift.ReleasedByType : "");
    setReleasedByID(gift.ReleasedByID && gift.ReleasedByID !== "0" ? String(gift.ReleasedByID) : "");
    setWhitelistOpen(gift.HasWhitelist);
    let buyers = "";
    try {
      const parsed = JSON.parse(gift.WhitelistBuyersJSON || "[]") as unknown;
      if (Array.isArray(parsed)) buyers = parsed.map((value) => String(value)).join("\n");
    } catch { buyers = ""; }
    setWhitelistBuyers(buyers);
    setWhitelistHidden(gift.WhitelistHidden); setWhitelistNumbered(gift.WhitelistNumbered);
    setWhitelistDisallowUpgrades(gift.WhitelistDisallowUpgrades); setWhitelistDisallowResell(gift.WhitelistDisallowResell);
    setWhitelistApply(gift.WhitelistApply);
    setAuctionEnabled(gift.Auction);
    setAuctionSlug(gift.AuctionSlug);
    setAuctionGiftsPerRound(String(gift.GiftsPerRound));
    setAuctionStartDate(gift.AuctionStartDate > 0 ? new Date(gift.AuctionStartDate * 1000).toISOString().slice(0, 16) : "");
    setAuctionRoundDuration(gift.AuctionRoundDuration > 0 ? String(gift.AuctionRoundDuration) : "");
    setAuctionRoundDurations(gift.AuctionRoundDurationsJSON || "");
    setAuctionRaiseMinBid(gift.AuctionRaiseMinBid);
    setAuctionRoundExtends(gift.AuctionRoundExtendsJSON || "");
    setAuctionMinBidAmount(gift.AuctionMinBidAmount && gift.AuctionMinBidAmount !== "0" ? gift.AuctionMinBidAmount : "");
    setAuctionEndDate(gift.AuctionStateEnd > 0 ? new Date(gift.AuctionStateEnd * 1000).toISOString().slice(0, 16) : "");
    setAuctionStatus(gift.AuctionStateStatus || "");
    setReason(""); setFile(null); setPreview(null); setImportError("");
    setImportOpen(true);
  }

  return (
    <PageFrame title={t("gifts.pageTitle")} eyebrow={t("gifts.eyebrow")} actions={<>
      <button className="btn" type="button" onClick={() => load()} disabled={busy}><RefreshCw size={15} /> {t("common.refresh")}</button>
      <button className="btn primary" type="button" onClick={startImport}><Plus size={15} /> {t("gifts.add")}</button>
    </>}>
      {error && <Alert>{error}</Alert>}
      <div className="metric-row gift-metrics">
        <Metric label={t("gifts.total")} value={String(gifts.length)} />
        <Metric label={t("gifts.enabled")} value={String(gifts.filter((gift) => gift.Enabled).length)} tone="good" />
                <Metric label={t("gifts.received")} value={gifts.reduce((sum, gift) => sum + BigInt(gift.ReceivedCount), 0n).toString()} />
        <Metric label={t("gifts.formats")} value="TGS / Lottie" />
      </div>
      <QueryPanel>
        <div className="toolbar">
          <label className="searchbox"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("gifts.searchPlaceholder")} /></label>
          <span className="gift-list-summary">{t("gifts.listSummary", { shown: visibleGifts.length, total: gifts.length })}</span>
        </div>
      </QueryPanel>
      <div className="table-wrap gift-table-wrap">
        <table className="data-table gift-table">
          <thead><tr><th>{t("gifts.animation")}</th><th>{t("gifts.idRevision")}</th><th>{t("gifts.title")}</th><th>{t("gifts.price")}</th><th>{t("gifts.source")}</th><th>{t("gifts.received")}</th><th>{t("common.status")}</th><th>{t("common.updatedAt")}</th><th>{t("common.actions")}</th></tr></thead>
          <tbody>
            {visibleGifts.map((gift) => (
              <tr className={gift.Enabled ? "" : "gift-row-disabled"} key={gift.GiftID}>
                <td><LottiePreview giftID={gift.GiftID} revision={gift.Revision} compact /></td>
                <td className="mono">{gift.GiftID} / {gift.Revision}</td>
                <td><strong className="gift-table-title">{gift.Title || `Gift #${gift.GiftID}`}</strong><span className="gift-sort-order">{t("gifts.sortOrder")}: {gift.SortOrder}</span></td>
                <td><strong className="gift-table-price">⭐ {gift.Stars}</strong><span className="gift-convert-price">→ {gift.ConvertStars}</span></td>
                <td><Badge>{gift.SourceFormat}</Badge><span className="gift-source-size">{formatBytes(gift.AnimationSize)}</span></td>
                <td>{gift.ReceivedCount}</td>
                <td><Badge tone={gift.Enabled ? "good" : "neutral"}>{gift.Enabled ? t("common.enabled") : t("common.disabled")}</Badge></td>
                <td>{formatDate(gift.UpdatedAt)}</td>
                <td><div className="gift-table-actions"><button className="btn compact-btn collectible-button" type="button" onClick={() => setCollectibleGift(gift)}><Gem size={13} />{t("collectibles.manage")}</button><button className="btn compact-btn" type="button" onClick={() => startEdit(gift)}><Pencil size={13} />{t("gifts.edit")}</button><button className="btn compact-btn" type="button" onClick={() => startRevision(gift)}>{t("gifts.replace")}</button><ActionButton compact tone="neutral" label={gift.Enabled ? t("gifts.disable") : t("gifts.enable")} path="/api/actions/set-gift-enabled" payload={() => ({ gift_id: gift.GiftID, enabled: !gift.Enabled })} onDone={() => void load()} /></div></td>
              </tr>
            ))}
            {visibleGifts.length === 0 && <EmptyRow colSpan={9} />}
          </tbody>
        </table>
      </div>

      {importOpen && createPortal(
        <div className="modal-backdrop" role="presentation">
                        <section className="modal command-modal gift-import-modal" role="dialog" aria-modal="true" aria-label={editGift ? t("gifts.editTitle", { id: editGift.GiftID }) : giftID !== "0" ? t("gifts.newRevision", { id: giftID }) : t("gifts.importTitle")}>
            <div className="modal-head">
                                <div><div className="eyebrow">{editGift ? t("gifts.editEyebrow") : t("gifts.importEyebrow")}</div><h2>{editGift ? t("gifts.editTitle", { id: editGift.GiftID }) : giftID !== "0" ? t("gifts.newRevision", { id: giftID }) : t("gifts.importTitle")}</h2></div>
              <button className="icon-btn" type="button" onClick={() => { setImportOpen(false); setEditGift(null); }} disabled={busy} aria-label={t("action.close")}><X size={15} /></button>
            </div>
            <div className="command-body gift-import-modal-body">
              <div className="command-steps">
                <div className={`command-step ${(editGift || (importSource === "official" ? sourceGiftID : file)) ? "done" : "active"}`}><span>1</span><strong>{t("gifts.stepDetails")}</strong></div>
                <div className={`command-step ${preview ? "done" : (editGift || (importSource === "official" ? sourceGiftID : file)) ? "active" : ""}`}><span>2</span><strong>{t("gifts.stepValidate")}</strong></div>
                <div className={`command-step ${preview ? "active" : ""}`}><span>3</span><strong>{editGift ? t("gifts.stepSave") : t("gifts.stepImport")}</strong></div>
              </div>
              {!editGift && <div className="gift-source-tabs">
                <button className={`btn ${importSource === "official" ? "primary" : ""}`} type="button" onClick={() => { setImportSource("official"); setPreview(null); }}>{t("gifts.officialSource")}</button>
                <button className={`btn ${importSource === "file" ? "primary" : ""}`} type="button" onClick={() => { setImportSource("file"); setPreview(null); }}>{t("gifts.fileSource")}</button>
              </div>}
              {!editGift && (importSource === "official" ? <section className="official-gift-picker">
                <div className="gift-import-note"><span>{t("gifts.officialHint")}</span><div className="gift-format-chips"><span>{officialGifts.length}</span><span>SHA-256</span></div></div>
                <div className="official-gift-tools">
                  <label className="searchbox"><Search size={15} /><input value={officialQuery} onChange={(e) => setOfficialQuery(e.target.value)} placeholder={t("gifts.officialSearch")} /></label>
                  <span>{t("gifts.officialResults", { shown: visibleOfficial.length, total: officialGifts.length })}</span>
                </div>
                <div className="official-gift-categories" role="group" aria-label={t("gifts.officialCategoryLabel")}>
                  {(["all", "upgrade", "craft", "basic"] as const).map((category) => (
                    <button key={category} className={officialCategory === category ? "active" : ""} type="button"
                      aria-pressed={officialCategory === category} onClick={() => setOfficialCategory(category)}>
                      {t(`gifts.officialCategory.${category}`)}<span>{officialCategoryCounts[category]}</span>
                    </button>
                  ))}
                </div>
                <div className="official-gift-list" role="listbox" aria-label={t("gifts.officialSelect")}>
                  {visibleOfficial.map((gift) => {
                    const selected = gift.source_gift_id === sourceGiftID;
                    return <button key={gift.source_gift_id} className={`official-gift-option ${selected ? "selected" : ""}`}
                      type="button" role="option" aria-selected={selected} onClick={() => chooseOfficial(gift)}>
                      <span className="official-gift-option-head">
                        <strong>{gift.title || t("gifts.officialUnnamed", { id: gift.source_gift_id })}</strong>
                        <span className="mono">#{gift.source_gift_id}</span>
                      </span>
                      <span className="official-gift-option-meta">
                        <span>⭐ {gift.stars}</span>
                        <span>{t("gifts.officialAttributes", { count: officialGiftAttributeCount(gift) })}</span>
                      </span>
                      <span className="official-gift-capabilities">
                        <span className={gift.can_upgrade ? "yes" : "no"}>{gift.can_upgrade ? t("gifts.canUpgrade") : t("gifts.cannotUpgrade")}</span>
                        <span className={gift.can_craft ? "craft" : "no"}>{gift.can_craft ? t("gifts.canCraft") : t("gifts.cannotCraft")}</span>
                      </span>
                    </button>;
                  })}
                  {visibleOfficial.length === 0 && <div className="official-gift-empty">{t("gifts.officialEmpty")}</div>}
                </div>
                {selectedOfficial && <div className="official-gift-selected">
                  <OfficialLottiePreview sourceGiftID={selectedOfficial.source_gift_id} />
                  <div><strong>{selectedOfficial.title || t("gifts.officialUnnamed", { id: selectedOfficial.source_gift_id })}</strong><span className="mono">{selectedOfficial.source_gift_id}</span><small>{selectedOfficial.model_count} {t("collectibles.models")} · {selectedOfficial.pattern_count} {t("collectibles.patterns")} · {selectedOfficial.backdrop_count} {t("collectibles.backdrops")}</small><span className="official-gift-capabilities"><span className={selectedOfficial.can_upgrade ? "yes" : "no"}>{selectedOfficial.can_upgrade ? t("gifts.canUpgrade") : t("gifts.cannotUpgrade")}</span><span className={selectedOfficial.can_craft ? "craft" : "no"}>{selectedOfficial.can_craft ? t("gifts.canCraft") : t("gifts.cannotCraft")}</span></span></div>
                </div>}
                {selectedOfficial && (selectedOfficial.can_upgrade || selectedOfficial.has_attributes) && <>
                  <label className="gift-switch"><input type="checkbox" checked={includeCollectible} onChange={(e) => { setIncludeCollectible(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.includeCollectible")}</span></label>
                  {includeCollectible && <div className="gift-fields-grid">
                    <label><span>{t("collectibles.upgradeStars")}</span><input type="number" min="0" value={upgradeStars} onChange={(e) => { setUpgradeStars(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("collectibles.supply")}</span><input type="number" min="1" value={supplyTotal} onChange={(e) => { setSupplyTotal(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("collectibles.slug")}</span><input value={slugPrefix} maxLength={48} onChange={(e) => { setSlugPrefix(e.target.value.toLowerCase()); setPreview(null); }} /></label>
                  </div>}
                </>}
              </section> : <>
                <div className="gift-import-note"><span>{t("gifts.importHint")}</span><div className="gift-format-chips" aria-label={t("gifts.formats")}><span>TGS</span><span>Lottie JSON</span></div></div>
                <label className={`gift-file-picker ${file ? "has-file" : ""}`}>
                  <input type="file" accept=".tgs,.json,.lottie,application/json,application/x-tgsticker" onChange={(e) => { setFile(e.target.files?.[0] ?? null); setPreview(null); }} />
                  <span className="gift-file-icon"><FileJson2 size={22} /></span>
                  <span className="gift-file-copy"><span className="gift-field-label">{t("gifts.animation")}</span><strong>{file ? file.name : t("gifts.filePrompt")}</strong><small>{file ? formatBytes(file.size) : t("gifts.fileHint")}</small></span>
                  <span className="gift-file-action">{file ? t("gifts.changeFile") : t("gifts.chooseFile")}</span>
                </label>
              </>)}
              {editGift && <div className="gift-import-note"><span>{t("gifts.editHint")}</span><div className="gift-format-chips"><span>#{editGift.GiftID}</span><span>r{editGift.Revision}</span></div></div>}
              <div className="gift-fields-grid">
                <label><span>{t("gifts.title")}</span><input value={title} maxLength={128} placeholder={t("gifts.titlePlaceholder")} onChange={(e) => { setTitle(e.target.value); setPreview(null); }} /></label>
                <label><span>{t("gifts.stars")}</span><input type="number" min="1" value={stars} onChange={(e) => { setStars(e.target.value); setPreview(null); }} /></label>
                <label><span>{t("gifts.convertStars")}</span><input type="number" min="0" value={convertStars} onChange={(e) => { setConvertStars(e.target.value); setPreview(null); }} /></label>
                <label><span>{t("gifts.sortOrder")}</span><input type="number" value={sortOrder} onChange={(e) => { setSortOrder(e.target.value); setPreview(null); }} /></label>
              </div>
              {/* Раунд 31: настройки выпуска — общие для копии из снимка и загрузки своего файла */}
              <div className="gift-settings-block">
                <div className="gift-settings-head">
                  <strong>{t("gifts.issueSettings")}</strong>
                  <span>{t("gifts.issueHint")}</span>
                </div>
                <div className="gift-fields-grid gift-fields-grid-wide">
                  <label><span>{t("gifts.availabilityTotal")}</span><input type="number" min="0" value={availabilityTotal} onChange={(e) => { setAvailabilityTotal(e.target.value); setPreview(null); }} /></label>
                  <label><span>{t("gifts.availabilityRemains")}</span><input type="number" min="0" value={availabilityRemains} onChange={(e) => { setAvailabilityRemains(e.target.value); setPreview(null); }} /></label>
                  <label><span>{t("gifts.perUserTotal")}</span><input type="number" min="0" value={perUserTotal} onChange={(e) => { setPerUserTotal(e.target.value); setPreview(null); }} /></label>
                  <label><span>{t("gifts.lockedUntilDate")}</span><input type="date" value={lockedUntilDate} onChange={(e) => { setLockedUntilDate(e.target.value); setPreview(null); }} /></label>
                  <label><span>{t("gifts.releasedByType")}</span>
                    <select value={releasedByType} onChange={(e) => { setReleasedByType(e.target.value as "" | "user" | "channel"); setPreview(null); }}>
                      <option value="">{t("gifts.releasedByNone")}</option>
                      <option value="user">{t("gifts.releasedByUser")}</option>
                      <option value="channel">{t("gifts.releasedByChannel")}</option>
                    </select>
                  </label>
                  <label><span>{t("gifts.releasedByID")}</span><input type="number" min="0" value={releasedByID} disabled={releasedByType === ""} onChange={(e) => { setReleasedByID(e.target.value); setPreview(null); }} /></label>
                </div>
                <div className="gift-settings-switches">
                  <label className="gift-switch"><input type="checkbox" checked={birthday} onChange={(e) => { setBirthday(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.birthday")}</span></label>
                  <label className="gift-switch"><input type="checkbox" checked={requirePremium} onChange={(e) => { setRequirePremium(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.requirePremium")}</span></label>
                </div>
                <div className="gift-whitelist-block">
                  <label className="gift-switch"><input type="checkbox" checked={whitelistOpen} onChange={(e) => { setWhitelistOpen(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.whitelistConfigure")}</span></label>
                  {whitelistOpen && <>
                    <label className="gift-reason-field"><span>{t("gifts.whitelistBuyers")}</span>
                      <textarea rows={3} value={whitelistBuyers} placeholder={t("gifts.whitelistBuyersPlaceholder")} onChange={(e) => { setWhitelistBuyers(e.target.value); setPreview(null); }} />
                    </label>
                    <div className="gift-settings-switches">
                      <label className="gift-switch"><input type="checkbox" checked={whitelistHidden} onChange={(e) => { setWhitelistHidden(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.whitelistHidden")}</span></label>
                      <label className="gift-switch"><input type="checkbox" checked={whitelistNumbered} onChange={(e) => { setWhitelistNumbered(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.whitelistNumbered")}</span></label>
                      <label className="gift-switch"><input type="checkbox" checked={whitelistDisallowUpgrades} onChange={(e) => { setWhitelistDisallowUpgrades(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.whitelistDisallowUpgrades")}</span></label>
                      <label className="gift-switch"><input type="checkbox" checked={whitelistDisallowResell} onChange={(e) => { setWhitelistDisallowResell(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.whitelistDisallowResell")}</span></label>
                      <label className="gift-switch"><input type="checkbox" checked={whitelistApply} onChange={(e) => { setWhitelistApply(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.whitelistApply")}</span></label>
                    </div>
                  </>}
                </div>
              </div>
              {/* Раунд 32: настройки аукциона (ревизия каталога) — общий блок
                  для обеих вкладок импорта и режима редактирования */}
                <div className="gift-settings-block">
                  <div className="gift-settings-head">
                    <strong>{t("gifts.auctionSettings")}</strong>
                    <span>{t("gifts.auctionHint")}</span>
                  </div>
                  <label className="gift-switch"><input type="checkbox" checked={auctionEnabled} onChange={(e) => { setAuctionEnabled(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.auctionEnable")}</span></label>
                  {auctionEnabled && <div className="gift-fields-grid gift-fields-grid-wide">
                    <label><span>{t("gifts.auctionSlug")}</span><input value={auctionSlug} maxLength={48} onChange={(e) => { setAuctionSlug(e.target.value.toLowerCase()); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auctionGiftsPerRound")}</span><input type="number" min="1" value={auctionGiftsPerRound} onChange={(e) => { setAuctionGiftsPerRound(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auctionStartDate")}</span><input type="datetime-local" value={auctionStartDate} onChange={(e) => { setAuctionStartDate(e.target.value); setPreview(null); }} /></label>
                  </div>}
                </div>
                {/* Раунд 33: рабочая строка star_gift_auctions — доступна и во вкладке
                    добавления (настройки применяются к созданному подарку сразу после
                    импорта), и в режиме редактирования; пустые поля «не менять» */}
                <div className="gift-settings-block">
                  <div className="gift-settings-head">
                    <strong>{t("gifts.auctionRuntime")}</strong>
                    <span>{t("gifts.auctionRuntimeHint")}</span>
                  </div>
                  <div className="gift-fields-grid gift-fields-grid-wide">
                    <label><span>{t("gifts.auctionRoundDuration")}</span><input type="number" min="1" placeholder={t("gifts.keepUnchanged")} value={auctionRoundDuration} onChange={(e) => { setAuctionRoundDuration(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auctionMinBid")}</span><input type="number" min="1" placeholder={t("gifts.keepUnchanged")} value={auctionMinBidAmount} onChange={(e) => { setAuctionMinBidAmount(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auctionEndDate")}</span><input type="datetime-local" value={auctionEndDate} onChange={(e) => { setAuctionEndDate(e.target.value); setPreview(null); }} /></label>
                    <label><span>{t("gifts.auctionStatus")}</span>
                      <select value={auctionStatus} onChange={(e) => { setAuctionStatus(e.target.value); setPreview(null); }}>
                        <option value="">{t("gifts.keepUnchanged")}</option>
                        <option value="pending">pending</option>
                        <option value="active">active</option>
                        <option value="completed">completed</option>
                        <option value="cancelled">cancelled</option>
                      </select>
                    </label>
                    <label className="gift-reason-field"><span>{t("gifts.auctionRoundDurations")}</span>
                      <textarea rows={2} placeholder='{"round_1":120,"round_2":300}' value={auctionRoundDurations} onChange={(e) => { setAuctionRoundDurations(e.target.value); setPreview(null); }} />
                    </label>
                    <label className="gift-reason-field"><span>{t("gifts.auctionRoundExtends")}</span>
                      <textarea rows={2} placeholder='{"round_2":{"window":30,"top":3,"threshold":120}}' value={auctionRoundExtends} onChange={(e) => { setAuctionRoundExtends(e.target.value); setPreview(null); }} />
                    </label>
                  </div>
                  <div className="gift-settings-switches">
                    <label className="gift-switch"><input type="checkbox" checked={auctionRaiseMinBid} onChange={(e) => { setAuctionRaiseMinBid(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.auctionRaiseMinBid")}</span></label>
                  </div>
                </div>
              <label className="gift-reason-field"><span>{t("gifts.reason")}</span><input value={reason} placeholder={t("gifts.reasonPlaceholder")} onChange={(e) => setReason(e.target.value)} /></label>
              <label className="gift-switch"><input type="checkbox" checked={enabled} onChange={(e) => { setEnabled(e.target.checked); setPreview(null); }} /><span className="gift-switch-track" aria-hidden="true"><span /></span><span>{t("gifts.enableAfterImport")}</span></label>
              {importError && <Alert>{importError}</Alert>}
              {preview && <div className="gift-validation"><div className="gift-validation-head"><CheckCircle2 size={17} /><div><strong>{t("gifts.validationReady")}</strong><span>{t("gifts.validationHint")}</span></div></div><pre>{JSON.stringify(preview.details, null, 2)}</pre></div>}
            </div>
            <div className="modal-actions">
              <button className="btn" type="button" onClick={() => { setImportOpen(false); setEditGift(null); }} disabled={busy}>{t("common.close")}</button>
              <button className="btn" type="button" onClick={validateImport} disabled={busy}>{busy ? <Loader2 className="spin" size={15} /> : <ShieldCheck size={15} />}{t("gifts.validate")}</button>
              <button className="btn primary" type="button" onClick={confirmImport} disabled={busy || !preview}><Upload size={15} />{editGift ? t("gifts.saveSettings") : t("gifts.confirmImport")}</button>
            </div>
          </section>
        </div>,
        document.body
      )}
      {collectibleGift && <GiftCollectiblesModal gift={collectibleGift} onClose={() => setCollectibleGift(null)} onPublished={() => void load()} />}
    </PageFrame>
  );
}
