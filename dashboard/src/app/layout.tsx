import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { Waypoints } from "lucide-react";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "Shadow Replicator Control Plane",
  description: "Live metrics and traffic controls for the Parallax shadow traffic replicator.",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col bg-background text-foreground">
        <nav className="sticky top-0 z-10 border-b border-border bg-background/80 backdrop-blur">
          <div className="mx-auto flex h-14 max-w-7xl items-center gap-3 px-6">
            <Waypoints className="size-5 text-accent" aria-hidden />
            <span className="text-sm font-semibold tracking-tight">Parallax</span>
            <span className="text-sm text-muted">Shadow Replicator Control Plane</span>
          </div>
        </nav>
        <div className="flex-1">{children}</div>
      </body>
    </html>
  );
}
