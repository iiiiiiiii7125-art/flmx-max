import { useEffect, useState } from "react";

import { checkSystemUpgrade } from "@/api";
import { siteConfig } from "@/config/site";
import {
  UPDATE_CHANNEL_CHANGED_EVENT,
  type UpdateReleaseChannel,
  getUpdateReleaseChannel,
} from "@/utils/version-update";

interface VersionFooterProps {
  version: string;
  containerClassName?: string;
  versionClassName?: string;
  poweredClassName?: string;
  updateBadgeClassName?: string;
}

export function VersionFooter({
  version,
  containerClassName,
  versionClassName,
  poweredClassName,
  updateBadgeClassName,
}: VersionFooterProps) {
  const [channel, setChannel] = useState<UpdateReleaseChannel>(
    getUpdateReleaseChannel(),
  );
  const [updateAvailable, setUpdateAvailable] = useState(false);
  const [latestUpdateVersion, setLatestUpdateVersion] = useState<string | null>(
    null,
  );

  useEffect(() => {
    const handleChannelChange = () => {
      setChannel(getUpdateReleaseChannel());
    };

    window.addEventListener(UPDATE_CHANNEL_CHANGED_EVENT, handleChannelChange);
    window.addEventListener("storage", handleChannelChange);

    return () => {
      window.removeEventListener(
        UPDATE_CHANNEL_CHANGED_EVENT,
        handleChannelChange,
      );
      window.removeEventListener("storage", handleChannelChange);
    };
  }, []);

  useEffect(() => {
    let active = true;

    const checkUpdate = async () => {
      // P3-3：更新检测改走后端检查接口，与配置页同一条路径（可经面板代理），
      // 不再由浏览器直连 api.github.com，避免两边结果不一致。
      // 请求失败（未登录/无权限/后端异常）时静默不显示红标。
      // 普通用户没有 /system 权限，silent 避免全局「权限不足」提示打扰。
      try {
        const response = await checkSystemUpgrade(channel, { silent: true });

        if (!active) {
          return;
        }

        if (
          response.code === 0 &&
          response.data?.hasUpdate &&
          response.data.latestVersion
        ) {
          setUpdateAvailable(true);
          setLatestUpdateVersion(response.data.latestVersion);
        } else {
          setUpdateAvailable(false);
          setLatestUpdateVersion(null);
        }
      } catch {
        if (!active) {
          return;
        }

        setUpdateAvailable(false);
        setLatestUpdateVersion(null);
      }
    };

    void checkUpdate();

    return () => {
      active = false;
    };
  }, [channel, version]);

  return (
    <div className={containerClassName}>
      <p className={versionClassName}>
        v{version}
        {updateAvailable && latestUpdateVersion && (
          <span className={updateBadgeClassName} role="status">
            {latestUpdateVersion}
          </span>
        )}
      </p>
      {siteConfig.hide_footer_brand !== true && (
        <p className={poweredClassName}>
          Powered by{" "}
          <a
            className="text-gray-500 dark:text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 transition-colors"
            href={siteConfig.github_repo}
            rel="noopener noreferrer"
            target="_blank"
          >
            FLMX
          </a>
        </p>
      )}
    </div>
  );
}
