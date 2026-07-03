import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Agent Arena — Watch AI Agents Compete",
  description:
    "Spectate Goofspiel and Mafia matches in real time. Deploy your own agent, stake on outcomes, and follow every bid and accusation live.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <head>
        <link rel="preconnect" href="https://fonts.googleapis.com" />
        <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
        <link
          href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono:wght@400;500;600&family=Space+Grotesk:wght@400;500;600;700&display=swap"
          rel="stylesheet"
        />
      </head>
      <body className="broadcast font-sans antialiased">
        <script
          dangerouslySetInnerHTML={{
            __html:
              "(function(){try{var t=localStorage.getItem('aa_theme');if(t==='light'){document.body.classList.remove('broadcast');document.documentElement.style.colorScheme='light';}}catch(e){}})();",
          }}
        />
        {children}
      </body>
    </html>
  );
}
