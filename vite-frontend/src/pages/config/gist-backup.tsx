import type { GistBackupStatus, GistBackupVersion } from "@/api";

import { useEffect, useState } from "react";
import toast from "react-hot-toast";

import {
  getGistBackupStatus,
  getGistBackupVersions,
  restoreGistBackup,
  saveGistBackup,
  uploadGistBackup,
} from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Input } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";

// PORT-3342/B1：备份上传 GitHub Gist（移植自上游 kure29/flyx 3.3.42，
// 仅上传侧；从 Gist 恢复不在本轮范围，恢复仍走手动导入流程）。
// PORT-3342/G-frontend（在 B1 前端基础上补齐）：Gist 版本列表展示
// （最近 10 个）+ 恢复弹窗 UI；恢复执行已由 G-recovery 接线
// （见 RESTORE_WIRING_PENDING = false 与弹窗确认按钮的 onRestore）。
// 另按用户钉死口径改 B1 文案：T-4 明文上传不放行
// （上传按钮禁用 + 保存/上传提交时明确拒绝，清除密码入口已撤下）、
// T-2 自动上传默认关、T-1 令牌为个人账号持有。
// 状态未加载时（含非管理员 403）渲染空，避免向非管理员暴露入口；
// 后端同样以 403 兜底。令牌与密码只上行、不回显，绝不在界面写真实令牌。

// PORT-3342/G-recovery：恢复执行已接线。false = 弹窗确认按钮调
// restoreGistBackup（POST /backup/gist/restore），后端走我方导入尾钩子
// 全集与 16 段全集；此前 G-frontend 占位态的 true 已由本批打开。
const RESTORE_WIRING_PENDING = false;

const INTERVALS: Array<[number, string]> = [
  [0, "关闭"],
  [6, "每 6 小时"],
  [12, "每 12 小时"],
  [24, "每天"],
  [168, "每周"],
];

const SELECT_CLASS =
  "h-10 w-full rounded-lg border border-divider bg-background px-2 text-sm";

function formatTime(ms: number): string {
  return new Date(ms).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** 数据备份与恢复卡片内的 Gist 上传区。 */
export function GistBackup() {
  const [status, setStatus] = useState<GistBackupStatus | null>(null);
  const [token, setToken] = useState("");
  const [password, setPassword] = useState("");
  // T-2：自动上传默认关——初始值 0（INTERVALS 首项「关闭」），
  // status 载入后以服务端 intervalHours 为准（未设置时服务端亦为 0）。
  const [intervalHours, setIntervalHours] = useState(0);
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [versions, setVersions] = useState<GistBackupVersion[] | null>(null);
  const [versionsLoading, setVersionsLoading] = useState(false);
  const [versionsError, setVersionsError] = useState("");
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [restoreVersion, setRestoreVersion] = useState("");

  const apply = (s: GistBackupStatus) => {
    setStatus(s);
    setIntervalHours(s.intervalHours);
    setToken("");
    setPassword("");
  };

  useEffect(() => {
    void getGistBackupStatus().then((res) => {
      if (res.code === 0 && res.data) apply(res.data);
    });
  }, []);

  if (!status) return null;

  const dirty =
    token.trim() !== "" ||
    password !== "" ||
    intervalHours !== status.intervalHours;

  // T-4 阻断态：已存密码或本次新填密码任一成立才可上传。
  const hasPassword = status.encrypted || password !== "";

  const save = async (patch: {
    clearToken?: boolean;
    clearPassword?: boolean;
  }): Promise<boolean> => {
    // T-4：明文上传不放行——提交时明确拒绝。已存密码或本次新填密码
    // 任一成立才放行；清除令牌（关闭 Gist 配置）本身不产生上传，故豁免。
    // 注意：UI 已撤下「不再加密」入口，不再发送 clearPassword: true；
    // 字段保留仅为与 B1 后端 save 契约兼容。
    const willHavePassword = Boolean(status?.encrypted) || password !== "";

    if (!patch.clearToken && !willHavePassword) {
      toast.error("必须设置加密密码才能上传：请先填写加密密码");

      return false;
    }
    setSaving(true);
    try {
      const res = await saveGistBackup({
        token: token.trim(),
        password,
        intervalHours,
        ...patch,
      });

      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "保存失败");

        return false;
      }
      apply(res.data);

      return true;
    } finally {
      setSaving(false);
    }
  };

  const upload = async () => {
    // 未保存的改动先保存，上传用最新设置。
    if (dirty && !(await save({}))) return;
    setUploading(true);
    try {
      const res = await uploadGistBackup();

      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "上传失败");
        void getGistBackupStatus().then((s) => {
          if (s.code === 0 && s.data) setStatus(s.data);
        });

        return;
      }
      setStatus(res.data.status);
      if (res.data.result.uploaded) toast.success("已上传到 Gist");
      else toast(res.data.result.message);
    } finally {
      setUploading(false);
    }
  };

  // PORT-3342/G-frontend：版本列表按需拉取（最近 10 个，后端已 cap 10，
  // 前端再 slice 一次兜底）。versions 端点由 G-后端批落定；端点未落定前
  // 拉取失败只显示错误文案，不关闭卡片、不阻断上传侧。
  const loadVersions = async () => {
    setVersionsLoading(true);
    setVersionsError("");
    try {
      const res = await getGistBackupVersions();

      if (res.code !== 0) {
        setVersionsError(res.msg || "获取版本列表失败");
        setVersions(null);

        return;
      }
      // G-recovery 接线修正：后端 versions 端点返回 {versions: [...]}，
      // 而非裸数组；两种形态都接受（裸数组为兼容兜底）。
      const raw = res.data as unknown;
      const list = (
        Array.isArray(raw)
          ? raw
          : ((raw as { versions?: GistBackupVersion[] }).versions ?? [])
      ).slice(0, 10);

      setVersions(list);
      if (list.length > 0 && restoreVersion === "") {
        setRestoreVersion(list[0].version);
      }
    } finally {
      setVersionsLoading(false);
    }
  };

  const openRestore = (version?: string) => {
    if (version) setRestoreVersion(version);
    setRestoreOpen(true);
    void loadVersions();
  };

  // PORT-3342/G-recovery：恢复执行（弹窗确认按钮的实际回调）。
  // 恢复前未保存的设置先保存（与上传口径一致：save 失败即阻断恢复，
  // 以免恢复用旧令牌/Gist id）；成功后 toast 并延迟刷新页面
  // （恢复可能使用户掉线，与上游恢复后刷新口径一致）。
  const restore = async (
    version: string,
    password: string,
  ): Promise<boolean> => {
    if (dirty && !(await save({}))) return false;

    const res = await restoreGistBackup({ version, password });

    if (res.code !== 0) {
      toast.error(res.msg || "恢复失败");

      return false;
    }
    toast.success("已从 Gist 恢复，页面即将刷新");
    window.setTimeout(() => window.location.reload(), 1500);

    return true;
  };

  const failed = status.lastError !== "";

  return (
    <div className="space-y-4">
      <h3 className="text-lg font-medium">上传到 GitHub Gist</h3>
      <p className="text-sm text-gray-600 dark:text-gray-400">
        全量备份加密后上传到私有 Gist，同一 Gist 每次上传产生
        一个新版本；自动上传默认关闭。明文上传不放行：必须设置加密密码
        才能上传。版本列表见下方，恢复入口见下方。
      </p>
      {status.configured && status.lastAt > 0 && (
        <p className="flex flex-wrap items-center gap-x-1 text-xs text-default-500">
          <span
            className={`mr-1 inline-block h-2 w-2 rounded-full ${failed ? "bg-danger" : "bg-success"}`}
          />
          {failed
            ? `上次上传失败（${formatTime(status.lastAt)}）：${status.lastError}`
            : `上次上传 ${formatTime(status.lastUpload || status.lastAt)} 成功`}
          {status.gistUrl && (
            <>
              <span>·</span>
              <a
                className="text-primary"
                href={status.gistUrl}
                rel="noreferrer"
                target="_blank"
              >
                查看 Gist
              </a>
            </>
          )}
        </p>
      )}

      <div className="space-y-1">
        <label className="text-sm font-medium" htmlFor="gist-token">
          GitHub 令牌
        </label>
        <Input
          aria-label="GitHub 令牌"
          autoComplete="off"
          id="gist-token"
          placeholder={status.configured ? status.tokenHint : "ghp_..."}
          value={token}
          variant="bordered"
          onChange={(e) => setToken(e.target.value)}
        />
        <p className="text-xs text-default-500">
          {status.configured ? (
            <>
              已保存，留空不修改 ·{" "}
              <button
                className="text-danger"
                type="button"
                onClick={() => void save({ clearToken: true })}
              >
                清除令牌
              </button>
            </>
          ) : (
            "个人账号 classic token 需 gist 权限，保存后不再显示"
          )}
        </p>
      </div>

      <div className="space-y-1">
        <label className="text-sm font-medium" htmlFor="gist-password">
          加密密码
        </label>
        <Input
          aria-label="加密密码"
          autoComplete="new-password"
          id="gist-password"
          placeholder={
            status.encrypted ? "已设置" : "必填：上传前必须设置加密密码"
          }
          type="password"
          value={password}
          variant="bordered"
          onChange={(e) => setPassword(e.target.value)}
        />
        <p className="text-xs text-default-500">
          {status.encrypted ? (
            "已设置，留空不修改 · 如需更换，直接输入新密码后保存（加密密码不可清除为空）"
          ) : (
            <span className="text-danger">
              必须设置加密密码才能上传（明文上传不放行）
            </span>
          )}
        </p>
      </div>

      <div className="space-y-1">
        <label className="text-sm font-medium" htmlFor="gist-interval">
          自动上传
        </label>
        <select
          aria-label="自动上传"
          className={SELECT_CLASS}
          id="gist-interval"
          value={intervalHours}
          onChange={(e) => setIntervalHours(Number(e.target.value))}
        >
          {INTERVALS.map(([h, label]) => (
            <option key={h} value={h}>
              {label}
            </option>
          ))}
        </select>
        <p className="text-xs text-default-500">
          默认关闭：未开启前不向外上传。
        </p>
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          color="primary"
          isDisabled={
            (!status.configured && token.trim() === "") || !hasPassword
          }
          isLoading={uploading}
          size="sm"
          onPress={upload}
        >
          立即上传
        </Button>
        {dirty && (
          <Button
            isLoading={saving}
            size="sm"
            variant="flat"
            onPress={() => void save({})}
          >
            保存
          </Button>
        )}
      </div>
      {!hasPassword && (
        <p className="text-xs text-danger">
          必须设置加密密码才能上传（明文上传不放行）：上传按钮已禁用，
          保存时亦会明确拒绝。
        </p>
      )}

      <div className="space-y-2 border-t border-divider pt-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-sm font-medium">Gist 版本（最近 10 个）</h4>
          <Button
            isDisabled={!status.configured}
            isLoading={versionsLoading}
            size="sm"
            variant="flat"
            onPress={() => void loadVersions()}
          >
            刷新版本列表
          </Button>
        </div>
        {!status.configured && (
          <p className="text-xs text-default-500">
            先保存令牌并完成一次上传后，可在此查看版本并打开恢复入口。
          </p>
        )}
        {versionsError !== "" && (
          <p className="text-xs text-danger">{versionsError}</p>
        )}
        {versions !== null && versions.length === 0 && (
          <p className="text-xs text-default-500">暂无版本。</p>
        )}
        {versions !== null && versions.length > 0 && (
          <ul className="space-y-1">
            {versions.map((v, i) => (
              <li
                key={v.version}
                className="flex flex-wrap items-center justify-between gap-2 text-sm"
              >
                <span>
                  {formatTime(v.committedAt)}
                  {i === 0 ? "（最新）" : ""}
                  <span className="ml-2 text-xs text-default-500">
                    {v.version}
                  </span>
                </span>
                <Button
                  size="sm"
                  variant="light"
                  onPress={() => openRestore(v.version)}
                >
                  恢复此版本
                </Button>
              </li>
            ))}
          </ul>
        )}
        <p className="text-xs text-default-500">
          恢复会按所选版本覆盖面板里的全部数据（Gist 设置除外）。
        </p>
      </div>

      {restoreOpen && (
        <GistRestoreModal
          encrypted={status.encrypted}
          initialVersion={restoreVersion}
          versions={versions}
          versionsError={versionsError}
          onClose={() => setRestoreOpen(false)}
          onReload={() => void loadVersions()}
          onRestore={restore}
        />
      )}
    </div>
  );
}

/** 从 Gist 恢复弹窗（PORT-3342/G-recovery 已接线：确认调 restore 端点）。 */
function GistRestoreModal({
  encrypted,
  initialVersion,
  versions,
  versionsError,
  onClose,
  onReload,
  onRestore,
}: {
  encrypted: boolean;
  initialVersion: string;
  versions: GistBackupVersion[] | null;
  versionsError: string;
  onClose: () => void;
  onReload: () => void;
  onRestore: (version: string, password: string) => Promise<boolean>;
}) {
  const [version, setVersion] = useState(initialVersion);
  const [password, setPassword] = useState("");
  const [restoring, setRestoring] = useState(false);

  useEffect(() => {
    if (version === "" && versions !== null && versions.length > 0) {
      setVersion(versions[0].version);
    }
  }, [version, versions]);

  return (
    <Modal
      isOpen
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      onOpenChange={(open) => !open && onClose()}
    >
      <ModalContent>
        <ModalHeader>从 Gist 恢复</ModalHeader>
        <ModalBody className="space-y-4">
          <div className="space-y-1">
            <label
              className="text-sm font-medium"
              htmlFor="gist-restore-version"
            >
              版本
            </label>
            <select
              aria-label="版本"
              className={SELECT_CLASS}
              disabled={versions === null}
              id="gist-restore-version"
              value={version}
              onChange={(e) => setVersion(e.target.value)}
            >
              {versions === null && <option>正在加载...</option>}
              {versions?.map((v, i) => (
                <option key={v.version} value={v.version}>
                  {formatTime(v.committedAt)}
                  {i === 0 ? "（最新）" : ""}
                </option>
              ))}
            </select>
            {versionsError !== "" && (
              <p className="text-xs text-danger">{versionsError}</p>
            )}
            {versions !== null && versions.length === 0 && (
              <p className="text-xs text-default-500">暂无版本。</p>
            )}
            {versionsError !== "" && (
              <Button size="sm" variant="flat" onPress={onReload}>
                重新加载版本
              </Button>
            )}
          </div>
          <div className="space-y-1">
            <label
              className="text-sm font-medium"
              htmlFor="gist-restore-password"
            >
              加密密码
            </label>
            <Input
              aria-label="加密密码"
              autoComplete="off"
              id="gist-restore-password"
              placeholder="上传时用的加密密码"
              type="password"
              value={password}
              variant="bordered"
              onChange={(e) => setPassword(e.target.value)}
            />
            <p className="text-xs text-default-500">
              {encrypted
                ? "恢复时需填写上传时用的加密密码。"
                : "当前 Gist 设置尚未标记加密；按明文不放行口径，上传均为加密，恢复时仍需填写上传时用的加密密码。"}
            </p>
          </div>
          <p className="text-xs text-default-500">
            恢复会覆盖面板里的全部数据（Gist 设置除外），之后可能需要重新登录。
          </p>
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            isDisabled={RESTORE_WIRING_PENDING || !version || restoring}
            onPress={async () => {
              setRestoring(true);
              try {
                if (await onRestore(version, password)) onClose();
              } finally {
                setRestoring(false);
              }
            }}
          >
            恢复
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
