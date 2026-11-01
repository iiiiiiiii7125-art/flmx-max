import { Navbar } from "@/components/navbar";

export default function DefaultLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="relative flex flex-col min-h-screen bg-mesh-gradient">
      <Navbar />
      <main className="container mx-auto max-w-7xl px-6 sm:px-8 flex-grow pt-6 sm:pt-10 animate-fade-in">
        {children}
      </main>
    </div>
  );
}
