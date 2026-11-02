import type { Dispatch, SetStateAction } from "react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Textarea } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@/shadcn-bridge/heroui/modal";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Spinner } from "@/shadcn-bridge/heroui/spinner";
import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";
import { Switch } from "@/shadcn-bridge/heroui/switch";
import { type NodeInstallMode, type ReleaseChannel } from "@/api";

export interface InstallCommandModalProps {
  currentNodeName: string;
  handleManualCopy: () => Promise<void>;
  installCommand: string;
  installCommandModal: boolean;
  /** R7-J-01: explicit plaintext warning from the install API, shown before copying. */
  installWarning?: string;
  setInstallCommandModal: Dispatch<SetStateAction<boolean>>;
  /** 安装形态（脚本 / Docker / Docker Compose）与容器调优开关。 */
  installMode: NodeInstallMode;
  installPrivileged: boolean;
  installModeLoading: boolean;
  onInstallModeChange: (mode: NodeInstallMode) => void;
  onInstallPrivilegedChange: (privileged: boolean) => void;
}

const INSTALL_MODE_OPTIONS: { key: NodeInstallMode; label: string }[] = [
  { key: "script", label: "脚本安装" },
  { key: "docker", label: "Docker" },
  { key: "docker-compose", label: "Docker Compose" },
];

const INSTALL_MODE_DESCRIPTIONS: Record<NodeInstallMode, string> = {
  script:
    "在节点服务器上下载安装脚本并以系统服务（systemd / OpenRC）方式安装 agent，配置与数据位于 /etc/flux_agent。",
  docker:
    "节点服务器需已安装 Docker。命令把节点配置写入 /etc/flmx_agent/config.json（权限 600），并以 host 网络 + NET_ADMIN 能力启动容器 flmx-agent；重复执行会重建该容器（升级/重装同理）。容器日志用 docker logs flmx-agent 查看。",
  "docker-compose":
    "节点服务器需已安装 Docker Compose。命令把节点配置写入 /etc/flmx_agent/config.json（权限 600），并把 Compose 文件写到 /opt/docker/flmx-agent/docker-compose.yaml 后启动；之后可用 docker compose -f 该文件管理。修改调优开关后重新获取命令再执行一次即可，只重写这一个 Compose 文件。",
};

/** The command that installs the agent on a node. */
export function InstallCommandModal({
  currentNodeName,
  handleManualCopy,
  installCommand,
  installCommandModal,
  installWarning,
  setInstallCommandModal,
  installMode,
  installPrivileged,
  installModeLoading,
  onInstallModeChange,
  onInstallPrivilegedChange,
}: InstallCommandModalProps) {
  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={installCommandModal}
      placement="center"
      scrollBehavior="inside"
      size="2xl"
      onClose={() => setInstallCommandModal(false)}
    >
      <ModalContent>
        <ModalHeader>安装命令 - {currentNodeName}</ModalHeader>
        <ModalBody>
          <div className="space-y-4">
            {installWarning ? (
              <div className="rounded-xl border border-danger-500/25 bg-danger-500/10 px-3 py-2 text-sm font-medium text-danger-800">
                ⚠️ {installWarning}
              </div>
            ) : null}
            <div className="space-y-2">
              <div className="text-sm font-medium text-foreground">安装方式</div>
              <div className="flex flex-wrap gap-2">
                {INSTALL_MODE_OPTIONS.map((option) => (
                  <Button
                    key={option.key}
                    color={installMode === option.key ? "primary" : "default"}
                    size="sm"
                    variant={installMode === option.key ? "solid" : "flat"}
                    onPress={() => onInstallModeChange(option.key)}
                  >
                    {option.label}
                  </Button>
                ))}
              </div>
              <p className="text-xs text-foreground">
                {INSTALL_MODE_DESCRIPTIONS[installMode]}
              </p>
              {installMode !== "script" ? (
                <div className="flex items-center gap-3 pt-1">
                  <Switch
                    isSelected={installPrivileged}
                    size="sm"
                    onValueChange={onInstallPrivilegedChange}
                  />
                  <div className="text-xs text-foreground">
                    <span className="font-semibold">服务器调优（特权容器）</span>
                    ：开启后容器以 --privileged 运行，面板的服务器调优
                    （BBR / sysctl / 内核模块加载）才能在容器内生效；不需要
                    调优时保持关闭即可。
                  </div>
                </div>
              ) : null}
            </div>
            <p className="text-sm text-foreground">
              请复制以下安装命令到服务器上执行：
            </p>
            <div className="relative">
              <Textarea
                readOnly
                className="font-mono text-sm"
                classNames={{
                  input: "font-mono text-sm",
                }}
                maxRows={10}
                minRows={6}
                value={installModeLoading ? "" : installCommand}
                variant="bordered"
              />
              {installModeLoading ? (
                <div className="absolute inset-0 flex items-center justify-center">
                  <Spinner size="sm" />
                </div>
              ) : (
                <Button
                  className="absolute top-2 right-2"
                  color="primary"
                  size="sm"
                  variant="flat"
                  onPress={handleManualCopy}
                >
                  复制
                </Button>
              )}
            </div>
            <div className="text-xs font-semibold text-foreground">
              💡 提示：如果复制按钮失效，请手动选择上方文本进行复制
            </div>
            <div className="text-xs font-semibold text-foreground">
              ✅ 执行后确认上线：命令执行完成只代表 agent
              已启动，不代表已连上面板。请回到节点列表确认该节点显示为
              <b>在线</b>；若未上线，在节点服务器执行{" "}
              {installMode === "script" ? (
                <span className="font-mono">journalctl -u flux_agent -n 100</span>
              ) : (
                <span className="font-mono">docker logs flmx-agent</span>
              )}{" "}
              查看连接日志，并核对「网站配置」中面板后端地址的 scheme
              是否与面板实际对外方式一致（HTTPS 面板须为 https://）。
            </div>
          </div>
        </ModalBody>
        <ModalFooter>
          <Button variant="flat" onPress={() => setInstallCommandModal(false)}>
            关闭
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}

export interface UpgradeNodeModalProps {
  handleConfirmUpgrade: () => Promise<void>;
  releases: {
    version: string;
    name: string;
    publishedAt: string;
    prerelease: boolean;
    channel: ReleaseChannel;
  }[];
  releasesLoading: boolean;
  selectedIds: Set<number>;
  selectedVersion: string;
  setSelectedVersion: Dispatch<SetStateAction<string>>;
  setUpgradeModalOpen: Dispatch<SetStateAction<boolean>>;
  upgradeModalOpen: boolean;
  upgradeTarget: "single" | "batch";
}

/** Pick a version and upgrade the agent on one or more nodes. */
export function UpgradeNodeModal({
  handleConfirmUpgrade,
  releases,
  releasesLoading,
  selectedIds,
  selectedVersion,
  setSelectedVersion,
  setUpgradeModalOpen,
  upgradeModalOpen,
  upgradeTarget,
}: UpgradeNodeModalProps) {
  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={upgradeModalOpen}
      placement="center"
      scrollBehavior="inside"
      size="md"
      onOpenChange={setUpgradeModalOpen}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h2 className="text-xl font-bold">
                {upgradeTarget === "batch"
                  ? `批量升级 (${selectedIds.size} 个节点)`
                  : "升级节点"}
              </h2>
            </ModalHeader>
            <ModalBody>
              {releasesLoading ? (
                <div className="flex justify-center py-8">
                  <Spinner size="lg" />
                </div>
              ) : (
                <div className="space-y-4">
                  <Select
                    label="选择版本"
                    placeholder="留空则使用最新版本"
                    selectedKeys={selectedVersion ? [selectedVersion] : []}
                    onSelectionChange={(keys) => {
                      const selected = Array.from(keys)[0] as string;

                      setSelectedVersion(selected || "");
                    }}
                  >
                    {releases.map((r) => (
                      <SelectItem key={r.version} textValue={r.version}>
                        <div className="flex justify-between items-center">
                          <span>{r.version}</span>
                          <span className="text-xs font-semibold text-foreground">
                            {r.publishedAt
                              ? new Date(r.publishedAt).toLocaleDateString()
                              : ""}
                            {r.channel === "dev" && (
                              <Chip
                                className="ml-1 shrink-0 whitespace-nowrap"
                                color="warning"
                                size="sm"
                                variant="flat"
                              >
                                测试
                              </Chip>
                            )}
                          </span>
                        </div>
                      </SelectItem>
                    ))}
                  </Select>
                  <p className="text-sm text-foreground">
                    {selectedVersion
                      ? `将升级到版本 ${selectedVersion}`
                      : "未选择版本，将自动使用最新版"}
                  </p>
                </div>
              )}
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={onClose}>
                取消
              </Button>
              <Button
                color="warning"
                isDisabled={releasesLoading}
                onPress={handleConfirmUpgrade}
              >
                确认升级
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
