import type { GistBackupStatus } from "@/api";

import { useEffect, useState } from "react";
import toast from "react-hot-toast";

import { getGistBackupStatus, saveGistBackup, uploadGistBackup } from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Input } from "@/shadcn-bridge/heroui/input";

// PORT-3342/B1：备份上传 GitHub Gist（移植自上游 kure29/flyx 3.3.42，
// 仅上传侧；从 Gist 恢复不在本轮范围，恢复仍走手动导入流程）。
// 状态未加载时（含非管理员 403）渲染空，避免向非管理员暴露入口；
// 后端同样以 403 兜底。令牌与密码只上行、不回显。

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
  const [intervalHours, setIntervalHours] = useState(0);
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);

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

  const save = async (patch: {
    clearToken?: boolean;
    clearPassword?: boolean;
  }): Promise<boolean> => {
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

  const failed = status.lastError !== "";

  return (
    <div className="space-y-4">
      <h3 className="text-lg font-medium">上传到 GitHub Gist</h3>
      <p className="text-sm text-gray-600 dark:text-gray-400">
        全量备份加密（可选）后上传到私有 Gist，同一 Gist 每次上传产生
        一个新版本；自动上传默认关闭。恢复仍用上方导入流程。
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
            "只需 gist 权限的令牌，保存后不再显示"
          )}
        </p>
      </div>

      <div className="space-y-1">
        <label className="text-sm font-medium" htmlFor="gist-password">
          加密密码（可选）
        </label>
        <Input
          aria-label="加密密码"
          autoComplete="new-password"
          id="gist-password"
          placeholder={status.encrypted ? "已设置" : "留空则不加密"}
          type="password"
          value={password}
          variant="bordered"
          onChange={(e) => setPassword(e.target.value)}
        />
        <p className="text-xs text-default-500">
          {status.encrypted ? (
            <>
              已设置，留空不修改 ·{" "}
              <button
                className="text-danger"
                type="button"
                onClick={() => void save({ clearPassword: true })}
              >
                不再加密
              </button>
            </>
          ) : (
            <>
              填了就先加密再上传。
              <span className="text-warning-600">
                不加密时，拿到 Gist 链接的人能看到节点密码等信息
              </span>
            </>
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
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          color="primary"
          isDisabled={!status.configured && token.trim() === ""}
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
    </div>
  );
}
