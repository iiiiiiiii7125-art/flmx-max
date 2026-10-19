// 占位组件：第四批（回程路由追踪，移植自 kure29/flyx）会替换为真实实现。
// 第二批先把 route 字段留空，此组件暂不渲染内容。
export function RouteTag({ route }: { route?: string; short?: boolean }) {
  if (!route) return null;
  return <span className="text-xs text-muted-foreground">{route}</span>;
}
