import { CheckCircle2, Copy, FileJson2, Gem, Loader2, Plus, ShieldCheck, Sparkles, Trash2, Upload, X } from "lucide-react";
import lottie from "lottie-web/build/player/lottie_light_canvas";
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { Alert, Badge } from "../components/ui";
import { useI18n } from "../i18n";
import type { CommandResult, StarGiftCollectibleAttributeRow, StarGiftCollectiblePreview, StarGiftCollectibleRevision, StarGiftCollectibleRevisionSummary, StarGiftRow } from "../types";

type AnimationData = Record<string, unknown>;
type AnimatedDraft = {
  key: string;
  name: string;
  // Раунд 33: тип редкости — permille (обычная, вес обычного апгрейда) или
  // именованная крафтовая (uncommon/rare/epic/legendary): rarity_permille в БД
  // NULL, атрибут помечается crafted.
  rarityKind: "permille" | "uncommon" | "rare" | "epic" | "legendary";
  rarity: string;
  sortOrder: string;
  file: File | null;
  animation: AnimationData | null;
  fileError: string;
  // Раунд 33: копия из другой ревизии — document_id исходного атрибута
  // (файл не загружается, документ и анимация переиспользуются).
  copyDocumentID: string;
  // Раунд 34: своя ревизия-источник копии (блоки могут браться из разных
  // ревизий); пусто — общий copy_from_revision_id.
  copyFromRevisionID: string;
  // Раунд 37: правка строки АКТИВНОЙ ревизии этого подарка «на месте».
  // Непустой id — строка обновляется (id сохраняется), файл опционален.
  attributeID: string;
};
type BackdropDraft = {
  key: string;
  name: string;
  backdropID: string;
  rarity: string;
  sortOrder: string;
  center: string;
  edge: string;
  pattern: string;
  text: string;
  // Раунд 37: правка строки фона активной ревизии; пусто — новая строка.
  attributeID: string;
};

let draftSequence = 0;
const nextKey = (kind: string) => `${kind}-${++draftSequence}`;
const backdropPalettes = [
  { center: "#6f5bea", edge: "#34278f", pattern: "#a89df5", text: "#ffffff" },
  { center: "#32a86b", edge: "#17613e", pattern: "#8ee0b3", text: "#ffffff" },
  { center: "#df8d2f", edge: "#8c421e", pattern: "#ffd08a", text: "#ffffff" },
  { center: "#d95878", edge: "#7b2944", pattern: "#f5a1b6", text: "#ffffff" }
];

// Раунд 33: перераспределение весов затрагивает только permille-атрибуты —
// крафтовые (именованная редкость) не участвуют в обычном апгрейде.
function rebalanceRarity<T extends { key: string; rarityKind?: string; rarity: string }>(rows: T[]): T[] {
  const permille = rows.filter((row) => (row.rarityKind ?? "permille") === "permille");
  if (!permille.length) return rows;
  const base = Math.floor(1000 / permille.length);
  const remainder = 1000 % permille.length;
  const weights = new Map(permille.map((row, index) => [row.key, String(base + (index < remainder ? 1 : 0))]));
  return rows.map((row) => ((row.rarityKind ?? "permille") === "permille" ? { ...row, rarity: weights.get(row.key) ?? row.rarity } : row));
}

const newAnimated = (kind: string, sortOrder: number): AnimatedDraft => ({
  key: nextKey(kind), name: "", rarityKind: "permille", rarity: "1", sortOrder: String(sortOrder),
  file: null, animation: null, fileError: "", copyDocumentID: "", copyFromRevisionID: "", attributeID: ""
});

function newBackdrop(rows: BackdropDraft[]): BackdropDraft {
  const backdropID = rows.reduce((maximum, row) => {
    const value = Number(row.backdropID);
    return Number.isInteger(value) ? Math.max(maximum, value) : maximum;
  }, 0) + 1;
  const colors = backdropPalettes[rows.length % backdropPalettes.length];
  return { key: nextKey("backdrop"), name: "", backdropID: String(backdropID), rarity: "1", sortOrder: String(rows.length), attributeID: "", ...colors };
}

const initialAnimated = (kind: string) => rebalanceRarity([newAnimated(kind, 0), newAnimated(kind, 1)]);
const initialBackdrops = () => {
  const first = newBackdrop([]);
  return rebalanceRarity([first, newBackdrop([first])]);
};

function AnimationPreview({ data, compact = false }: { data: AnimationData; compact?: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!host.current) return;
    const player = lottie.loadAnimation({ container: host.current, renderer: "canvas", loop: true, autoplay: true, animationData: structuredClone(data) });
    return () => player.destroy();
  }, [data]);
  return <div className={`collectible-animation ${compact ? "compact" : ""}`} ref={host} />;
}

function RemoteAnimation({ giftID, attribute }: { giftID: string; attribute: StarGiftCollectibleAttributeRow }) {
  const [data, setData] = useState<AnimationData | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let cancelled = false;
    setFailed(false);
    api.giftCollectibleAnimation(giftID, attribute.kind as "model" | "pattern", attribute.id)
      .then((value) => { if (!cancelled) setData(value); })
      .catch(() => { if (!cancelled) setFailed(true); });
    return () => { cancelled = true; };
  }, [giftID, attribute.id, attribute.kind]);
  if (failed) return <div className="collectible-animation compact failed">!</div>;
  if (!data) return <div className="collectible-animation compact loading"><Loader2 className="spin" size={15} /></div>;
  return <AnimationPreview data={data} compact />;
}

export async function parseAnimationFile(file: File): Promise<AnimationData> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let raw: Uint8Array = bytes;
  // Валидный TGS — это gzip-поток (магия 0x1f 0x8b). Проверка байта должна
  // совпадать с реальной магией gzip: ошибочный байт оставлял бинарный файл
  // JSON.parse и падал «unexpected character at line 1 column 1».
  if (bytes.length >= 2 && bytes[0] === 0x1f && bytes[1] === 0x8b) {
    if (!("DecompressionStream" in window)) throw new Error("This browser cannot preview TGS files");
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"));
    raw = new Uint8Array(await new Response(stream).arrayBuffer());
  }
  const parsed: unknown = JSON.parse(new TextDecoder().decode(raw));
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Invalid Lottie JSON");
  return parsed as AnimationData;
}

const colorNumber = (value: string) => Number.parseInt(value.replace("#", ""), 16);
const hexColor = (value: number | undefined, fallback: string) =>
  value === undefined || value === null ? fallback : `#${(value & 0xffffff).toString(16).padStart(6, "0")}`;
const rarityLabel = (attribute: StarGiftCollectibleAttributeRow) => attribute.rarity_kind === "permille" ? `${attribute.rarity_permille}‰` : attribute.rarity_kind;

const RARITY_KINDS = ["permille", "uncommon", "rare", "epic", "legendary"] as const;

// Раунд 37: предзаполнение черновика из АКТИВНОЙ ревизии подарка — правка
// «на месте». Каждая строка несёт attribute_id (id строки атрибута), поэтому
// публикация обновляет существующие строки и сервер (крафт, превью улучшения,
// rarity-пул, выпущенные подарки) продолжает работать с ними.
const animatedFromPreview = (rows: StarGiftCollectibleAttributeRow[] | undefined, kind: "model" | "pattern"): AnimatedDraft[] =>
  (rows ?? []).filter((row) => row.kind === kind && row.id).map((row, index) => ({
    key: nextKey(kind),
    name: row.name,
    rarityKind: (RARITY_KINDS as readonly string[]).includes(row.rarity_kind) ? row.rarity_kind as AnimatedDraft["rarityKind"] : "permille",
    rarity: String(row.rarity_permille || 1),
    sortOrder: String(row.sort_order ?? index),
    file: null,
    animation: null,
    fileError: "",
    copyDocumentID: "",
    copyFromRevisionID: "",
    attributeID: row.id
  } as AnimatedDraft));

const backdropsFromPreview = (rows: StarGiftCollectibleAttributeRow[] | undefined): BackdropDraft[] =>
  (rows ?? []).filter((row) => row.kind === "backdrop" && row.id).map((row, index) => ({
    key: nextKey("backdrop"),
    name: row.name,
    backdropID: String(row.backdrop_id ?? index + 1),
    rarity: String(row.rarity_permille || 1),
    sortOrder: String(row.sort_order ?? index),
    center: hexColor(row.center_color, backdropPalettes[index % backdropPalettes.length].center),
    edge: hexColor(row.edge_color, backdropPalettes[index % backdropPalettes.length].edge),
    pattern: hexColor(row.pattern_color, backdropPalettes[index % backdropPalettes.length].pattern),
    text: hexColor(row.text_color, "#ffffff"),
    attributeID: row.id
  } as BackdropDraft));
// Раунд 34: ключи блоков — единый селект источника копирования на блок.
type CopyBlockKind = "models" | "patterns" | "backdrops";

export function GiftCollectiblesModal({ gift, onClose, onPublished }: { gift: StarGiftRow; onClose: () => void; onPublished: () => void }) {
  const { t } = useI18n();
  const [active, setActive] = useState<StarGiftCollectiblePreview | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<CommandResult | null>(null);
  const [upgradeStars, setUpgradeStars] = useState("100");
  const [supplyTotal, setSupplyTotal] = useState("1000");
  const [slugPrefix, setSlugPrefix] = useState(`gift-${gift.GiftID}`);
  const [reason, setReason] = useState("");
  const [models, setModels] = useState<AnimatedDraft[]>(() => initialAnimated("model"));
  const [patterns, setPatterns] = useState<AnimatedDraft[]>(() => initialAnimated("pattern"));
  const [backdrops, setBackdrops] = useState<BackdropDraft[]>(initialBackdrops);
  // Раунд 33/34: копирование атрибутов из другой ревизии («правка
  // существующей ревизии» = копия активной ревизии + правки поверх). Раунд 35:
  // список ревизий строится по SLUG коллекции (один slug может публиковаться
  // разными подарками — новый подарок ещё не имеет собственных ревизий);
  // ошибки загрузки больше не глотаются молча.
  const [revisions, setRevisions] = useState<StarGiftCollectibleRevisionSummary[]>([]);
  const [revisionsScope, setRevisionsScope] = useState<"slug" | "gift" | "all">("gift");
  const [revisionsError, setRevisionsError] = useState("");
  const [revisionsBusy, setRevisionsBusy] = useState(false);
  const [copyRevisionID, setCopyRevisionID] = useState("");
  const [blockCopySource, setBlockCopySource] = useState<Record<CopyBlockKind, string>>({ models: "", patterns: "", backdrops: "" });
  const [copyBusy, setCopyBusy] = useState<CopyBlockKind | "all" | "">("");
  const [copyError, setCopyError] = useState("");
  const [copySource, setCopySource] = useState<StarGiftCollectibleRevision | null>(null);
  // Раунд 34: лимит возможных улучшенных подарков ОПУБЛИКОВАННОЙ ревизии
  // (нужен, когда supply каталога увеличен — иначе апгрейд блокируется).
  const [activeSupply, setActiveSupply] = useState("");
  const [supplyPreview, setSupplyPreview] = useState<CommandResult | null>(null);
  const [supplyBusy, setSupplyBusy] = useState(false);
  const [supplyError, setSupplyError] = useState("");

  const refreshActive = () => {
    api.giftCollectibles(gift.GiftID).then((value) => {
      setActive(value);
      if (value.found) setActiveSupply(String(value.supply_total ?? ""));
    }).catch(() => undefined);
  };

  useEffect(() => {
    let cancelled = false;
    api.giftCollectibles(gift.GiftID).then((value) => {
      if (cancelled) return;
      setActive(value);
      if (value.found) {
        setUpgradeStars(String(value.upgrade_stars ?? 100));
        setSupplyTotal(String(value.supply_total ?? 1000));
        setSlugPrefix(value.slug_prefix ?? `gift-${gift.GiftID}`);
        setCopyRevisionID(String(value.revision ?? ""));
        setActiveSupply(String(value.supply_total ?? ""));
        // Раунд 37: черновик = активная ревизия. Публикация правит строки
        // «на месте»; «опубликовать новую» доступна через копирование из
        // другой ревизии (записи без attribute_id добавляются как новые).
        setModels(animatedFromPreview(value.models, "model"));
        setPatterns(animatedFromPreview(value.patterns, "pattern"));
        setBackdrops(backdropsFromPreview(value.backdrops));
      }
    }).catch((err) => setError(errorMessage(err))).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [gift.GiftID]);

  // Раунд 35: список ревизий перезагружается при смене slug коллекции
  // (с дебаунсом на ввод) — источник копирования ищется по slug_prefix,
  // поэтому коллекция видна даже для подарка без собственных ревизий.
  const loadRevisions = (slug: string) => {
    setRevisionsBusy(true); setRevisionsError("");
    api.giftCollectibleRevisions(gift.GiftID, slug).then((value) => {
      setRevisions(value.revisions ?? []);
      setRevisionsScope(value.scope === "all" || value.scope === "slug" ? value.scope : "gift");
    }).catch((err) => {
      setRevisions([]);
      setRevisionsScope("gift");
      setRevisionsError(errorMessage(err) || t("collectibles.copyLoadFailed"));
    }).finally(() => setRevisionsBusy(false));
  };

  useEffect(() => {
    const timer = setTimeout(() => loadRevisions(slugPrefix), 350);
    return () => clearTimeout(timer);
  }, [gift.GiftID, slugPrefix]);

  const rarityTotals = useMemo(() => ({
    models: models.reduce((sum, value) => sum + (value.rarityKind === "permille" ? Number(value.rarity || 0) : 0), 0),
    patterns: patterns.reduce((sum, value) => sum + (value.rarityKind === "permille" ? Number(value.rarity || 0) : 0), 0),
    backdrops: backdrops.reduce((sum, value) => sum + Number(value.rarity || 0), 0)
  }), [models, patterns, backdrops]);

  // Раунд 37: количество возможных вариантов NFT — произведение количеств
  // атрибутов (модели × узоры × фоны). Сервер записывает его в ревизию
  // при публикации; здесь показываем живой расчёт для оператора.
  const upgradeVariants = models.length * patterns.length * backdrops.length;
  const storedVariants = active?.found ? (active.upgrade_variants ?? 0) : 0;

  const invalidate = () => setPreview(null);
  const updateAnimated = (kind: "models" | "patterns", key: string, patch: Partial<AnimatedDraft>, rebalance = false) => {
    const setter = kind === "models" ? setModels : setPatterns;
    setter((rows) => {
      const updated = rows.map((row) => row.key === key ? { ...row, ...patch } : row);
      return rebalance ? rebalanceRarity(updated) : updated;
    });
    invalidate();
  };

  async function chooseFile(kind: "models" | "patterns", row: AnimatedDraft, file: File | null) {
    // Загрузка своего файла отменяет режим копии для этой записи; правка
    // строки активной ревизии (attribute_id) сохраняется — файл ЗАМЕНЯЕТ
    // анимацию существующей строки.
    updateAnimated(kind, row.key, { file, copyDocumentID: "", copyFromRevisionID: "", animation: null, fileError: "" });
    if (!file) return;
    try {
      const animation = await parseAnimationFile(file);
      updateAnimated(kind, row.key, { animation, fileError: "" });
    } catch (err) {
      updateAnimated(kind, row.key, { animation: null, fileError: errorMessage(err) });
    }
  }

  // Раунд 34: анимированные записи копии — из указанной ревизии.
  // Раунд 37: копия из СОВПАДАЮЩЕЙ с активной ревизии этого подарка — это
  // правка «на месте»: строки получают attribute_id вместо copy_document_id,
  // и публикация обновляет существующие строки, а не плодит дубликаты.
  const isActiveRevision = (revision: StarGiftCollectibleRevision): boolean =>
    !!active?.found && revision.gift_id === gift.GiftID && revision.status === "published" && revision.revision === active.revision;

  const animatedFromRevision = (revision: StarGiftCollectibleRevision, kind: "model" | "pattern", sourceID: string): AnimatedDraft[] => {
    const rows = kind === "model" ? revision.models : revision.patterns;
    const inPlace = isActiveRevision(revision);
    return (rows ?? []).filter((row) => row.kind === kind && row.document_id).map((row, index) => ({
      key: nextKey(kind),
      name: row.name,
      rarityKind: (RARITY_KINDS as readonly string[]).includes(row.rarity_kind) ? row.rarity_kind as AnimatedDraft["rarityKind"] : "permille",
      rarity: String(row.rarity_permille || 1),
      sortOrder: String(row.sort_order ?? index),
      file: null,
      animation: null,
      fileError: "",
      copyDocumentID: inPlace ? "" : row.document_id ?? "",
      copyFromRevisionID: inPlace ? "" : sourceID,
      attributeID: inPlace ? row.id : ""
    } as AnimatedDraft));
  };

  const backdropsFromRevision = (revision: StarGiftCollectibleRevision): BackdropDraft[] => {
    const inPlace = isActiveRevision(revision);
    return (revision.backdrops ?? []).map((row, index) => ({
      key: nextKey("backdrop"),
      name: row.name,
      backdropID: String(row.backdrop_id ?? index + 1),
      rarity: String(row.rarity_permille || 1),
      sortOrder: String(row.sort_order ?? index),
      center: hexColor(row.center_color, backdropPalettes[index % backdropPalettes.length].center),
      edge: hexColor(row.edge_color, backdropPalettes[index % backdropPalettes.length].edge),
      pattern: hexColor(row.pattern_color, backdropPalettes[index % backdropPalettes.length].pattern),
      text: hexColor(row.text_color, "#ffffff"),
      attributeID: inPlace ? row.id : ""
    } as BackdropDraft));
  };

  // Раунд 34: КОПИРОВАНИЕ ОДНИМ БЛОКОМ (модели / фоны / узоры) из выбранной
  // ревизии — список ревизий рядом, остальные блоки не затрагиваются.
  async function copyBlock(kind: CopyBlockKind, sourceID: string) {
    const id = sourceID.trim();
    if (!id) return;
    setCopyBusy(kind); setCopyError("");
    try {
      const revision = await api.giftCollectibleRevision(id);
      if (!revision.found) throw new Error(t("collectibles.copyFailed"));
      if (kind === "models") setModels(animatedFromRevision(revision, "model", id));
      else if (kind === "patterns") setPatterns(animatedFromRevision(revision, "pattern", id));
      else setBackdrops(backdropsFromRevision(revision));
      if (kind === "models" || kind === "patterns") {
        // Копии из ревизии без файлов — общий источник больше не нужен,
        // у каждой записи свой copy_from_revision_id.
        setCopySource(null);
      }
      setPreview(null);
    } catch (err) {
      setCopyError(errorMessage(err) || t("collectibles.copyFailed"));
    } finally {
      setCopyBusy("");
    }
  }

  // Раунд 33/34: полное копирование ревизии — все три блока + основные поля
  // («правка существующей ревизии» без перезаливки файлов).
  async function copyFromRevision(sourceID: string) {
    const id = sourceID.trim();
    if (!id) return;
    setCopyBusy("all"); setCopyError("");
    try {
      const revision = await api.giftCollectibleRevision(id);
      if (!revision.found) throw new Error(t("collectibles.copyFailed"));
      setModels(animatedFromRevision(revision, "model", id));
      setPatterns(animatedFromRevision(revision, "pattern", id));
      setBackdrops(backdropsFromRevision(revision));
      setUpgradeStars(String(revision.upgrade_stars ?? "0"));
      setSupplyTotal(String(revision.supply_total ?? 1000));
      setSlugPrefix(revision.slug_prefix ?? `gift-${gift.GiftID}`);
      setCopySource(revision);
      setPreview(null);
    } catch (err) {
      setCopyError(errorMessage(err) || t("collectibles.copyFailed"));
      setCopySource(null);
    } finally {
      setCopyBusy("");
    }
  }

  // Раунд 34: применить лимит возможных улучшенных подарков к ОПУБЛИКОВАННОЙ
  // ревизии (двухфазная команда: проверка → подтверждение).
  async function applySupply() {
    const value = Number(activeSupply);
    if (!reason.trim()) { setSupplyError(t("action.reasonRequired")); return; }
    if (!Number.isInteger(value) || value <= 0) { setSupplyError(t("collectibles.supplyInvalid")); return; }
    setSupplyBusy(true); setSupplyError(""); setSupplyPreview(null);
    try {
      const base = { gift_id: gift.GiftID, supply_total: value, reason: reason.trim() };
      const dry = await api.setGiftCollectibleSupply({ ...base, command_id: "", confirm: false });
      if (dry.error) throw new Error(dry.error);
      const result = await api.setGiftCollectibleSupply({ ...base, command_id: dry.command_id, confirm: true });
      if (result.error) throw new Error(result.error);
      setSupplyPreview(result);
      refreshActive();
      onPublished();
    } catch (err) {
      setSupplyError(errorMessage(err));
    } finally {
      setSupplyBusy(false);
    }
  }

  function buildForm(confirm: boolean, commandID = "") {
    if (!reason.trim()) throw new Error(t("action.reasonRequired"));
    if (models.length < 2 || patterns.length < 2 || backdrops.length < 2) throw new Error(t("collectibles.minimumAttributes"));
    const backdropIDs = backdrops.map((row) => Number(row.backdropID));
    if (new Set(backdropIDs).size !== backdropIDs.length) throw new Error(t("collectibles.duplicateBackdropID"));
    for (const row of [...models, ...patterns]) {
      // Раунд 37: правка строки активной ревизии (attribute_id) файл не
      // требует — анимация остаётся прежней; файл заменяет её.
      if (!row.copyDocumentID && !row.attributeID && !row.file) throw new Error(t("collectibles.fileRequired"));
    }
    const form = new FormData();
    const animatedMetadata = (rows: AnimatedDraft[]) => rows.map((row) => {
      const entry: Record<string, unknown> = {
        name: row.name.trim(),
        rarity_permille: row.rarityKind === "permille" ? Number(row.rarity) : 0,
        sort_order: Number(row.sortOrder)
      };
      if (row.rarityKind !== "permille") entry.rarity_kind = row.rarityKind;
      // Копия из ревизии: файл не прикладывается, документ переиспользуется.
      // Раунд 34: у каждой копии может быть СВОЯ ревизия-источник.
      if (row.copyDocumentID) {
        entry.copy_document_id = row.copyDocumentID;
        if (row.copyFromRevisionID) entry.copy_from_revision_id = row.copyFromRevisionID;
      } else if (row.attributeID) {
        // Раунд 37: правка существующей строки активной ревизии.
        entry.attribute_id = row.attributeID;
        if (row.file) entry.file_key = row.key;
      } else {
        entry.file_key = row.key;
      }
      return entry;
    });
    const metadata: Record<string, unknown> = {
      command_id: commandID, reason: reason.trim(), confirm,
      upgrade_stars: upgradeStars, supply_total: Number(supplyTotal), slug_prefix: slugPrefix.trim().toLowerCase(),
      models: animatedMetadata(models), patterns: animatedMetadata(patterns),
      backdrops: backdrops.map((row) => ({
        name: row.name.trim(), backdrop_id: Number(row.backdropID), rarity_permille: Number(row.rarity), sort_order: Number(row.sortOrder),
        center_color: colorNumber(row.center), edge_color: colorNumber(row.edge), pattern_color: colorNumber(row.pattern), text_color: colorNumber(row.text),
        ...(row.attributeID ? { attribute_id: row.attributeID } : {})
      }))
    };
    // Общий источник (полное копирование ревизии) — только если часть копий
    // не указала собственный copy_from_revision_id.
    const source = copySource;
    if (source && [...models, ...patterns].some((row) => row.copyDocumentID && !row.copyFromRevisionID)) {
      metadata.copy_from_revision_id = source.revision_id;
    }
    form.set("metadata", JSON.stringify(metadata));
    for (const row of [...models, ...patterns]) if (row.file && !row.copyDocumentID) form.set(row.key, row.file, row.file.name);
    return form;
  }

  async function validate() {
    setBusy(true); setError(""); setPreview(null);
    try { setPreview(await api.publishGiftCollectibles(gift.GiftID, buildForm(false))); }
    catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }

  async function publish() {
    if (!preview) return;
    setBusy(true); setError("");
    try {
      await api.publishGiftCollectibles(gift.GiftID, buildForm(true, preview.command_id));
      onPublished(); onClose();
    } catch (err) { setError(errorMessage(err)); }
    finally { setBusy(false); }
  }

  // Раунд 35: в подписи источника показывается и подарок-владелец ревизии —
  // одна коллекция (slug) может публиковаться разными подарками.
  const revisionOptions = revisions.map((revision) => (
    <option key={revision.revision_id} value={revision.revision_id}>
      r{revision.revision} · #{revision.gift_id} · {revision.slug_prefix} · {t(`collectibles.status.${revision.status === "published" ? "published" : "draft"}`)} · {revision.model_count}/{revision.pattern_count}/{revision.backdrop_count}
    </option>
  ));

  // Раунд 35/36: подсказка/ошибка списка ревизий под выбором источника.
  // Раунд 36: сервер пишет scope выборки — "slug"|"gift"|"all" (fallback
  // «все ревизии» срабатывает, когда ни slug, ни gift_id не дали результата).
  const revisionListStatus = <>
    {revisionsError && <Alert>{revisionsError}</Alert>}
    {!revisionsError && !revisionsBusy && revisions.length === 0 && <span className="collectible-copy-empty">{t("collectibles.copyEmpty")}</span>}
    {!revisionsError && !revisionsBusy && revisions.length > 0 && <span className="collectible-copy-empty">
      {revisionsScope === "all"
        ? t("collectibles.scopeAll", { count: revisions.length })
        : t("collectibles.scopeMatch", { count: revisions.length })}
    </span>}
  </>;

  // Раунд 34: инструменты копирования в шапке блока — выбор ревизии + кнопка.
  const blockCopyTools = (kind: CopyBlockKind, disabled = false) => (
    <div className="collectible-copy-tools">
      <select
        className="collectible-copy-select"
        value={blockCopySource[kind]}
        onChange={(e) => setBlockCopySource((state) => ({ ...state, [kind]: e.target.value }))}
        disabled={copyBusy !== "" || revisionsBusy || disabled}
        aria-label={t("collectibles.copySelect")}
      >
        <option value="">{revisionsBusy ? t("collectibles.copyLoading") : t("collectibles.copySelect")}</option>
        {revisionOptions}
      </select>
      <button className="btn compact-btn" type="button"
        disabled={copyBusy !== "" || revisionsBusy || disabled || !blockCopySource[kind]}
        onClick={() => void copyBlock(kind, blockCopySource[kind])}>
        {copyBusy === kind ? <Loader2 className="spin" size={13} /> : <Copy size={13} />}{t("collectibles.copyLoad")}
      </button>
    </div>
  );

  const renderAnimatedRows = (kind: "models" | "patterns", rows: AnimatedDraft[], setRows: (rows: AnimatedDraft[]) => void) => (
    <section className="collectible-section">
      <div className="collectible-section-head">
        <div><strong>{t(`collectibles.${kind}`)}</strong><span>{t("collectibles.rarityHint")}</span></div>
        <div className="collectible-section-tools">
          <Badge tone={rarityTotals[kind] > 0 ? "good" : "neutral"}>{rarityTotals[kind]}‰</Badge>
          {blockCopyTools(kind)}
          <button className="btn compact-btn" type="button" onClick={() => { setRows(rebalanceRarity([...rows, newAnimated(kind === "models" ? "model" : "pattern", rows.length)])); invalidate(); }}><Plus size={13} />{t("collectibles.addAttribute")}</button>
        </div>
      </div>
      <div className="collectible-rows">
        {rows.map((row, index) => <div className="collectible-row animated" key={row.key}>
          <div className="collectible-row-index">{index + 1}</div>
          <label><span>{t("common.name")}</span><input value={row.name} maxLength={128} onChange={(e) => updateAnimated(kind, row.key, { name: e.target.value })} /></label>
          <label><span>{t("collectibles.rarityKind")}</span>
            <select value={row.rarityKind} onChange={(e) => updateAnimated(kind, row.key, { rarityKind: e.target.value as AnimatedDraft["rarityKind"], rarity: "1" }, true)}>
              {RARITY_KINDS.map((value) => <option key={value} value={value}>{t(`collectibles.rarityKind.${value}`)}</option>)}
            </select>
          </label>
          {row.rarityKind === "permille"
            ? <label><span>{t("collectibles.rarity")}</span><input type="number" min="1" max="1000" value={row.rarity} onChange={(e) => updateAnimated(kind, row.key, { rarity: e.target.value })} /></label>
            : <label><span>&nbsp;</span><Badge>{t("collectibles.craftedBadge")}</Badge></label>}
          <label><span>{t("gifts.sortOrder")}</span><input type="number" value={row.sortOrder} onChange={(e) => updateAnimated(kind, row.key, { sortOrder: e.target.value })} /></label>
          {row.copyDocumentID
            ? <label className="collectible-file copied"><span>{t("gifts.animation")}</span><em><Copy size={13} />{t("collectibles.copiedBadge")} #{row.copyDocumentID}</em></label>
            : row.attributeID && !row.file
              ? <label className="collectible-file copied"><span>{t("gifts.animation")}</span><em><Gem size={13} />{t("collectibles.storedAnimation")} #{row.attributeID}</em></label>
              : <label className="collectible-file"><span>{t("gifts.animation")}</span><input type="file" accept=".tgs,.json,.lottie,application/json,application/x-tgsticker" onChange={(e) => void chooseFile(kind, row, e.target.files?.[0] ?? null)} /><em><FileJson2 size={13} />{row.file?.name ?? t("gifts.chooseFile")}</em></label>}
          <div className="collectible-inline-preview">{row.animation
            ? <AnimationPreview data={row.animation} compact />
            : row.attributeID && !row.file && (kind === "models" || kind === "patterns")
              ? <RemoteAnimation giftID={gift.GiftID} attribute={{ id: row.attributeID, kind: kind === "models" ? "model" : "pattern", name: row.name, rarity_kind: row.rarityKind, rarity_permille: Number(row.rarity) || 1, crafted: row.rarityKind !== "permille", official_document_id: "", sort_order: Number(row.sortOrder) } as StarGiftCollectibleAttributeRow} />
              : <Sparkles size={16} />}</div>
          <button className="icon-btn danger" type="button" disabled={rows.length <= 2} onClick={() => { setRows(rebalanceRarity(rows.filter((value) => value.key !== row.key))); invalidate(); }} aria-label={t("collectibles.remove")}><Trash2 size={14} /></button>
          {row.fileError && <span className="collectible-file-error">{row.fileError}</span>}
        </div>)}
      </div>
    </section>
  );

  return createPortal(<div className="modal-backdrop" role="presentation">
    <section className="modal command-modal collectible-modal" role="dialog" aria-modal="true" aria-label={t("collectibles.title", { id: gift.GiftID })}>
      <div className="modal-head">
        <div><div className="eyebrow">{t("collectibles.eyebrow")}</div><h2>{t("collectibles.title", { id: gift.GiftID })}</h2><p>{gift.Title || `Gift #${gift.GiftID}`}</p></div>
        <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("action.close")}><X size={15} /></button>
      </div>
      <div className="command-body collectible-modal-body">
        {loading ? <div className="collectible-loading"><Loader2 className="spin" />{t("common.loading")}</div> : active?.found ? <section className="collectible-active">
          <div className="collectible-active-head"><div><Gem size={18} /><div><strong>{t("collectibles.activeRevision", { revision: active.revision ?? 0 })}</strong><span>{active.slug_prefix} · ⭐ {active.upgrade_stars} · {active.issued} / {active.supply_total}</span></div></div><div className="collectible-active-badges"><Badge tone="neutral" title={t("collectibles.variantsHint")}>{t("collectibles.variants", { count: storedVariants })}</Badge><Badge tone="good">{t("collectibles.published")}</Badge></div></div>
          {/* Раунд 34: лимит возможных улучшенных подарков — правится на
              опубликованной ревизии (supply каталога может быть увеличен). */}
          <div className="collectible-supply-row">
            <label>
              <span>{t("collectibles.supplyConfig")}</span>
              <input type="number" min="1" value={activeSupply} onChange={(e) => { setActiveSupply(e.target.value); setSupplyPreview(null); }} />
            </label>
            <button className="btn compact-btn" type="button" disabled={supplyBusy || !activeSupply || Number(activeSupply) === Number(active.supply_total)} onClick={() => void applySupply()}>
              {supplyBusy ? <Loader2 className="spin" size={13} /> : <ShieldCheck size={13} />}{t("collectibles.supplyApply")}
            </button>
            <span className="collectible-supply-hint">{t("collectibles.supplyConfigHint")}</span>
          </div>
          {supplyError && <Alert>{supplyError}</Alert>}
          {supplyPreview && <div className="gift-validation compact"><div className="gift-validation-head"><CheckCircle2 size={15} /><div><strong>{t("collectibles.supplyApplied")}</strong><span>{t("collectibles.supply")}: {String(supplyPreview.details?.supply_total ?? activeSupply)} · {t("collectibles.issued")}: {String(supplyPreview.details?.issued ?? active.issued ?? 0)}</span></div></div></div>}
          <div className="collectible-active-grid">
            {[...(active.models ?? []), ...(active.patterns ?? [])].map((attribute) => <article key={`${attribute.kind}-${attribute.id}`}><RemoteAnimation giftID={gift.GiftID} attribute={attribute} /><div><strong>{attribute.name}{attribute.crafted && <Badge>{t("collectibles.craftedBadge")}</Badge>}</strong><span>{t(`collectibles.${attribute.kind}`)} · {rarityLabel(attribute)}</span></div></article>)}
            {(active.backdrops ?? []).map((attribute) => <article key={`backdrop-${attribute.id}`}><div className="collectible-backdrop-preview" style={{ background: `radial-gradient(circle, #${(attribute.center_color ?? 0).toString(16).padStart(6, "0")}, #${(attribute.edge_color ?? 0).toString(16).padStart(6, "0")})`, color: `#${(attribute.text_color ?? 0xffffff).toString(16).padStart(6, "0")}` }}>Aa</div><div><strong>{attribute.name}</strong><span>{t("collectibles.backdrop")} · {rarityLabel(attribute)}</span></div></article>)}
          </div>
        </section> : <div className="collectible-empty"><Gem size={22} /><div><strong>{t("collectibles.noPool")}</strong><span>{t("collectibles.noPoolHint")}</span></div></div>}

        <section className="collectible-definition">
          {/* Раунд 37: при активной ревизии черновик предзаполнен её строками,
              и публикация правит их «на месте» — новая ревизия не создаётся. */}
          <div className="collectible-definition-head"><div><strong>{active?.found ? t("collectibles.editTitle") : t("collectibles.publishNew")}</strong><span>{active?.found ? t("collectibles.editHint") : t("collectibles.immutableHint")}</span></div><div className="gift-format-chips"><span>TGS</span><span>Lottie JSON</span><Badge tone={upgradeVariants > 0 ? "good" : "neutral"} title={t("collectibles.variantsHint")}>{t("collectibles.variants", { count: upgradeVariants })}</Badge></div></div>
          <div className="gift-fields-grid collectible-main-fields">
            <label><span>{t("collectibles.upgradeStars")}</span><input type="number" min="0" value={upgradeStars} onChange={(e) => { setUpgradeStars(e.target.value); invalidate(); }} /></label>
            <label><span>{t("collectibles.supply")}</span><input type="number" min="1" value={supplyTotal} onChange={(e) => { setSupplyTotal(e.target.value); invalidate(); }} /></label>
            <label><span>{t("collectibles.slug")}</span><input value={slugPrefix} maxLength={48} onChange={(e) => { setSlugPrefix(e.target.value.toLowerCase()); invalidate(); }} /></label>
            <label><span>{t("gifts.reason")}</span><input value={reason} maxLength={1000} placeholder={t("gifts.reasonPlaceholder")} onChange={(e) => setReason(e.target.value)} /></label>
          </div>
          {/* Раунд 33/34: полное копирование ревизии + поблочное копирование
              в шапках секций ниже. Раунд 35: список ревизий строится по slug
              коллекции (см. поле slug выше) — ревизии других подарков той же
              коллекции тоже доступны для копирования. */}
          <div className="gift-whitelist-block collectible-copy-block">
            <div className="gift-settings-head">
              <strong>{t("collectibles.copyTitle")}</strong>
              <span>{t("collectibles.copyHint")}</span>
            </div>
            <div className="collectible-copy-tools collectible-copy-tools-all">
              <select
                className="collectible-copy-select"
                value={copyRevisionID}
                onChange={(e) => setCopyRevisionID(e.target.value)}
                disabled={copyBusy !== "" || revisionsBusy}
                aria-label={t("collectibles.copySelect")}
              >
                <option value="">{revisionsBusy ? t("collectibles.copyLoading") : t("collectibles.copySelect")}</option>
                {revisionOptions}
              </select>
              <button className="btn" type="button" disabled={copyBusy !== "" || revisionsBusy || !copyRevisionID.trim()} onClick={() => void copyFromRevision(copyRevisionID)}>
                {copyBusy === "all" ? <Loader2 className="spin" size={15} /> : <Copy size={15} />}{t("collectibles.copyLoadAll")}
              </button>
            </div>
            {revisionListStatus}
            {copyError && <Alert>{copyError}</Alert>}
          </div>
          {renderAnimatedRows("models", models, setModels)}
          {renderAnimatedRows("patterns", patterns, setPatterns)}
          <section className="collectible-section">
            <div className="collectible-section-head"><div><strong>{t("collectibles.backdrops")}</strong><span>{t("collectibles.colorHint")}</span></div><div className="collectible-section-tools"><Badge tone={rarityTotals.backdrops > 0 ? "good" : "neutral"}>{rarityTotals.backdrops}‰</Badge>{blockCopyTools("backdrops")}<button className="btn compact-btn" type="button" onClick={() => { setBackdrops(rebalanceRarity([...backdrops, newBackdrop(backdrops)])); invalidate(); }}><Plus size={13} />{t("collectibles.addAttribute")}</button></div></div>
            <div className="collectible-rows">{backdrops.map((row, index) => <div className="collectible-row backdrop" key={row.key}>
              <div className="collectible-row-index">{index + 1}</div>
              <label><span>{t("common.name")}</span><input value={row.name} maxLength={128} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, name: e.target.value } : value)); invalidate(); }} /></label>
              <label><span>{t("collectibles.backdropID")}</span><input type="number" min="0" value={row.backdropID} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, backdropID: e.target.value } : value)); invalidate(); }} /></label>
              <label><span>{t("collectibles.rarity")}</span><input type="number" min="1" max="1000" value={row.rarity} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, rarity: e.target.value } : value)); invalidate(); }} /></label>
              <label><span>{t("gifts.sortOrder")}</span><input type="number" value={row.sortOrder} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, sortOrder: e.target.value } : value)); invalidate(); }} /></label>
              {(["center", "edge", "pattern", "text"] as const).map((field) => <label className="collectible-color" key={field}><span>{t(`collectibles.color.${field}`)}</span><input type="color" value={row[field]} onChange={(e) => { setBackdrops(backdrops.map((value) => value.key === row.key ? { ...value, [field]: e.target.value } : value)); invalidate(); }} /></label>)}
              <div className="collectible-backdrop-preview" style={{ background: `radial-gradient(circle, ${row.center}, ${row.edge})`, color: row.text }}>Aa</div>
              <button className="icon-btn danger" type="button" disabled={backdrops.length <= 2} onClick={() => { setBackdrops(rebalanceRarity(backdrops.filter((value) => value.key !== row.key))); invalidate(); }} aria-label={t("collectibles.remove")}><Trash2 size={14} /></button>
            </div>)}</div>
          </section>
        </section>
        {error && <Alert>{error}</Alert>}
        {preview && <div className="gift-validation"><div className="gift-validation-head"><CheckCircle2 size={17} /><div><strong>{t("collectibles.validationReady")}</strong><span>{t("collectibles.validationHint")}</span></div></div><pre>{JSON.stringify(preview.details, null, 2)}</pre></div>}
      </div>
      <div className="modal-actions">
        <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
        <button className="btn" type="button" onClick={validate} disabled={busy}>{busy ? <Loader2 className="spin" size={15} /> : <ShieldCheck size={15} />}{t("gifts.validate")}</button>
        <button className="btn primary" type="button" onClick={publish} disabled={busy || !preview}><Upload size={15} />{active?.found ? t("collectibles.saveRevision") : t("collectibles.publish")}</button>
      </div>
    </section>
  </div>, document.body);
}
