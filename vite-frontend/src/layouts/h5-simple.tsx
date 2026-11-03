import React from "react";
import { useNavigate } from "react-router-dom";

import { Button } from "@/shadcn-bridge/heroui/button";
import { BackIcon } from "@/components/icons";
import { BrandLogo } from "@/components/brand-logo";
import { siteConfig } from "@/config/site";
import { useScrollTopOnPathChange } from "@/hooks/useScrollTopOnPathChange";

export default function H5SimpleLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const navigate = useNavigate();

  useScrollTopOnPathChange();

  const handleBack = () => {
    navigate("/profile");
  };

  return (
    <div className="flex flex-col min-h-screen bg-mesh-gradient">
      {/* 顶部导航栏 */}
      <header className="[background:var(--glass-bg)] backdrop-blur-[18px] backdrop-saturate-[1.5] border-b border-white/45 h-14 safe-top flex-shrink-0 flex items-center justify-between px-4 relative z-10">
        <div className="flex items-center gap-2">
          <Button isIconOnly size="sm" variant="light" onPress={handleBack}>
            <BackIcon className="w-5 h-5" />
          </Button>
          <div className="flex items-center gap-2 glass-surface rounded-full py-1 pl-1.5 pr-3">
            <BrandLogo size={20} />
            <h1 className="text-sm font-bold text-foreground">
              {siteConfig.name}
            </h1>
          </div>
        </div>

        <div className="flex items-center gap-2" />
      </header>

      {/* 主内容区域 */}
      <main className="flex-1 pb-0">{children}</main>
    </div>
  );
}
