import { useCallback, useEffect, useRef, useState } from "react";

import { getAnnouncement, type AnnouncementData } from "@/api";
import { getUserId } from "@/utils/session";

// R8-044：公告已读标记按用户分键——旧版全局单键
// flmx_announcement_seen_time 让同一浏览器先后登录的两个账号共用
// 已读状态，先登录者关掉弹窗后，后登录者看不到该版公告弹窗。
// 现按 JWT 用户 ID 分键；取不到用户 ID（token 不可用）时回落旧键名
// 保持原行为。旧键仅作回落名保留、不再被已识别用户读写，既有值
// 不会引发错误（升级后各用户首次会重新看到当前版公告一次）。
const ANNOUNCEMENT_SEEN_STORAGE_KEY = "flmx_announcement_seen_time";

const announcementSeenStorageKey = (): string => {
  const userId = getUserId();

  return userId === null
    ? ANNOUNCEMENT_SEEN_STORAGE_KEY
    : `${ANNOUNCEMENT_SEEN_STORAGE_KEY}:${userId}`;
};

export interface AnnouncementState {
  announcement: AnnouncementData | null;
  isAnnouncementModalOpen: boolean;
  setIsAnnouncementModalOpen: (isOpen: boolean) => void;
  dismissAnnouncementModal: () => void;
}

/**
 * 公告横幅/弹窗共用逻辑（U1-R2B3-01）：普通用户总览与管理员总览
 * 用同一份——取公告、update_time 比对已读键、弹窗开关、「关闭」
 * 只关本次不落已读、「不再提示」才写已读键（AP-4，取代
 * R7-F5-03 的「关闭即落已读」）。
 */
export const useAnnouncement = (): AnnouncementState => {
  const [announcement, setAnnouncement] = useState<AnnouncementData | null>(
    null,
  );
  const [isAnnouncementModalOpen, setIsAnnouncementModalOpen] = useState(false);
  const isMountedRef = useRef(true);

  const loadAnnouncement = useCallback(async () => {
    try {
      const res = await getAnnouncement();

      if (!isMountedRef.current) {
        return;
      }

      if (res.code === 0 && res.data && res.data.enabled === 1) {
        setAnnouncement(res.data);

        try {
          const storedTimeStr = localStorage.getItem(
            announcementSeenStorageKey(),
          );
          const storedTime = storedTimeStr ? parseInt(storedTimeStr, 10) : 0;
          const updateTime = res.data.update_time || 0;

          if (updateTime > storedTime) {
            setIsAnnouncementModalOpen(true);
          }
        } catch {
          setIsAnnouncementModalOpen(true);
        }
      } else {
        setAnnouncement(null);
      }
    } catch {
      if (isMountedRef.current) {
        setAnnouncement(null);
      }
    }
  }, []);

  const dismissAnnouncementModal = useCallback(() => {
    setIsAnnouncementModalOpen(false);
    if (announcement && announcement.update_time) {
      try {
        localStorage.setItem(
          announcementSeenStorageKey(),
          announcement.update_time.toString(),
        );
      } catch {
        // Ignore localStorage write failures and keep the modal dismissed.
      }
    }
  }, [announcement]);

  useEffect(() => {
    isMountedRef.current = true;
    void loadAnnouncement();

    return () => {
      isMountedRef.current = false;
    };
  }, [loadAnnouncement]);

  return {
    announcement,
    isAnnouncementModalOpen,
    setIsAnnouncementModalOpen,
    dismissAnnouncementModal,
  };
};
