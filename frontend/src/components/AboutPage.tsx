import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { DragDropMedia } from "@/components/DragDropTextarea";
import { openExternal } from "@/lib/utils";
import { GetOSInfo } from "../../wailsjs/go/main/App";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Bug, Lightbulb, ExternalLink, CircleHelp, Github, Users } from "lucide-react";
import { ScrollArea } from "@/components/ui/scroll-area";

interface AboutPageProps {
    version: string;
}

export function AboutPage({ version }: AboutPageProps) {
    const [os, setOs] = useState("Unknown");
    const [location, setLocation] = useState("Unknown");
    const [activeTab, setActiveTab] = useState("bug_report");
    const [bugType, setBugType] = useState("Track");
    const [problem, setProblem] = useState("");
    const [spotifyUrl, setSpotifyUrl] = useState("");
    const [bugContext, setBugContext] = useState("");
    const [featureDesc, setFeatureDesc] = useState("");
    const [useCase, setUseCase] = useState("");
    const [featureContext, setFeatureContext] = useState("");

    useEffect(() => {
        const fetchOS = async () => {
            try {
                const info = await GetOSInfo();
                setOs(info);
            }
            catch {
                const userAgent = window.navigator.userAgent;
                if (userAgent.indexOf("Win") !== -1)
                    setOs("Windows");
                else if (userAgent.indexOf("Mac") !== -1)
                    setOs("macOS");
                else if (userAgent.indexOf("Linux") !== -1)
                    setOs("Linux");
            }
        };
        fetchOS();
        const fetchLocation = async () => {
            try {
                const response = await fetch('https://ipapi.co/json/');
                if (response.ok) {
                    const data = await response.json();
                    const city = data.city || '';
                    const region = data.region || '';
                    const country = data.country_name || '';
                    const parts = [city, region, country].filter(Boolean);
                    setLocation(parts.join(', ') || 'Unknown');
                }
                else {
                    const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
                    setLocation(timezone);
                }
            }
            catch {
                const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
                setLocation(timezone);
            }
        };
        fetchLocation();
    }, []);
    const faqs = [
        {
            q: "Is this software free?",
            a: "Yes. This software is completely free. You do not need an account, login, or subscription. All you need is an internet connection."
        },
        {
            q: "Can using this software get my Spotify account suspended or banned?",
            a: "No. This software has no connection to your Spotify account. Spotify data is obtained through reverse engineering of the Spotify Web Player, not through user authentication."
        },
        {
            q: "Where does the audio come from?",
            a: "The audio is fetched using third-party APIs."
        },
        {
            q: "Why does metadata fetching sometimes fail?",
            a: "This usually happens because your IP address has been rate-limited. You can wait and try again later, or use a VPN to bypass the rate limit."
        },
        {
            q: "Why does Windows Defender or antivirus flag or delete the file?",
            a: "This is a false positive. It likely happens because the executable is compressed using UPX. If you are concerned, you can fork the repository and build the software yourself from source."
        },
        {
            q: "Why does the app sometimes fail to obtain a token?",
            a: "The target website uses Cloudflare protection. The app attempts to bypass it using ChromeDriver, which may require multiple retries."
        },
        {
            q: "Why do I get a 500 error when a download fails?",
            a: "A 500 error indicates a server-side issue. This is outside of my control."
        }
    ];
    const authors = [
        {
            name: "Laynholt",
            role: "Current maintainer",
            url: "https://github.com/Laynholt"
        },
        {
            name: "afkarxyz",
            role: "Original project author",
            url: "https://github.com/afkarxyz/"
        }
    ];
    const handleSubmit = () => {
        const title = activeTab === "bug_report"
            ? `[Bug Report] ${problem.substring(0, 50)}${problem.length > 50 ? "..." : ""}`
            : `[Feature Request] ${featureDesc.substring(0, 50)}${featureDesc.length > 50 ? "..." : ""}`;
        const bodyContent = activeTab === "bug_report"
            ? (() => {
            const contextContent = bugContext.trim() ? bugContext.trim() : "Type here or send screenshot/recording";
            return `### [Bug Report]

#### Problem
${problem || "Type here"}

#### Type
${bugType}

#### Spotify URL
${spotifyUrl || "Type here"}

#### Additional Context
${contextContent}

#### Environment
- SpotiDownloader Version: ${version}
- OS: ${os}
- Location: ${location}`;
        })()
            : (() => {
            const contextContent = featureContext.trim() ? featureContext.trim() : "Type here or send screenshot/recording";
            return `### [Feature Request]

#### Description
${featureDesc || "Type here"}

#### Use Case
${useCase || "Type here"}

#### Additional Context
${contextContent}`;
        })();
        const params = new URLSearchParams({
            title: title,
            body: bodyContent
        });
        const url = `https://github.com/Laynholt/spotify-downloader/issues/new?${params.toString()}`;
        openExternal(url);
    };
    return (<div className={`flex flex-col space-y-4 ${activeTab === "faq" ? "h-[calc(100vh-10rem)]" : ""}`}>
        <div className="flex items-center justify-between shrink-0">
            <h2 className="text-2xl font-bold tracking-tight">About</h2>
        </div>

        <div className="flex gap-2 border-b shrink-0">
            <Button variant={activeTab === "bug_report" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("bug_report")} className="rounded-b-none">
                <Bug className="h-4 w-4"/>
                Bug Report
            </Button>
            <Button variant={activeTab === "feature_request" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("feature_request")} className="rounded-b-none">
                <Lightbulb className="h-4 w-4"/>
                Feature Request
            </Button>
            <Button variant={activeTab === "faq" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("faq")} className="rounded-b-none">
                <CircleHelp className="h-4 w-4"/>
                FAQ
            </Button>
            <Button variant={activeTab === "authors" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("authors")} className="rounded-b-none">
                <Users className="h-4 w-4"/>
                Authors
            </Button>
        </div>

        <div className={`flex-1 min-h-0 ${activeTab === "faq" ? "overflow-hidden" : ""}`}>
            {activeTab === "bug_report" && (<div className="flex flex-col">
                    <div className="space-y-4 pt-4 flex flex-col">
                        <div className="mt-4 pr-2">
                           <div className="grid md:grid-cols-3 gap-6">
                                    <div className="space-y-2 flex flex-col">
                                        <Label>Problem</Label>
                                        <Textarea className="h-56 resize-none" placeholder="Describe the problem..." value={problem} onChange={e => setProblem(e.target.value)}/>
                                    </div>
                                    <div className="space-y-2 flex flex-col">
                                            <Label>Additional Context</Label>
                                            <DragDropMedia className="min-h-[14rem]" value={bugContext} onChange={setBugContext}/>
                                        </div>
                                    <div className="space-y-4 flex flex-col">
                                        <div className="space-y-2">
                                            <Label>Type</Label>
                                            <ToggleGroup type="single" value={bugType} onValueChange={(val) => {
                if (val)
                    setBugType(val);
            }} className="justify-start w-full cursor-pointer">
                                                <ToggleGroupItem value="Track" className="flex-1 cursor-pointer" aria-label="Toggle track">Track</ToggleGroupItem>
                                                <ToggleGroupItem value="Album" className="flex-1 cursor-pointer" aria-label="Toggle album">Album</ToggleGroupItem>
                                                <ToggleGroupItem value="Playlist" className="flex-1 cursor-pointer" aria-label="Toggle playlist">Playlist</ToggleGroupItem>
                                                <ToggleGroupItem value="Artist" className="flex-1 cursor-pointer" aria-label="Toggle artist">Artist</ToggleGroupItem>
                                            </ToggleGroup>
                                        </div>
                                        <div className="space-y-2">
                                            <Label>Spotify URL</Label>
                                            <Input placeholder="https://open.spotify.com/..." value={spotifyUrl} onChange={e => setSpotifyUrl(e.target.value)}/>
                                        </div>
                                    </div>
                                </div>
                            </div>
                    </div>
                    <div className="flex justify-center pt-4 shrink-0">
                        <Button className="w-[200px] cursor-pointer gap-2" onClick={handleSubmit}>
                            <ExternalLink className="h-4 w-4"/> Create Issue on GitHub
                        </Button>
                    </div>
                </div>)}

            {activeTab === "feature_request" && (<div className="flex flex-col">
                    <div className="space-y-4 pt-4 flex flex-col">
                        <div className="mt-4 pr-2">
                            <div className="grid md:grid-cols-3 gap-6">
                                    <div className="space-y-2 flex flex-col">
                                        <Label>Description</Label>
                                        <Textarea className="h-56 resize-none" placeholder="Describe your feature request..." value={featureDesc} onChange={e => setFeatureDesc(e.target.value)}/>
                                    </div>
                                    <div className="space-y-2 flex flex-col">
                                        <Label>Use Case</Label>
                                        <Textarea className="h-56 resize-none" placeholder="How would this feature be useful?" value={useCase} onChange={e => setUseCase(e.target.value)}/>
                                    </div>
                                    <div className="space-y-2 flex flex-col">
                                        <Label>Additional Context</Label>
                                        <DragDropMedia className="min-h-[14rem]" value={featureContext} onChange={setFeatureContext}/>
                                    </div>
                                </div>
                            </div>
                    </div>
                    <div className="flex justify-center pt-4 shrink-0">
                        <Button className="w-[200px] cursor-pointer gap-2" onClick={handleSubmit}>
                            <ExternalLink className="h-4 w-4"/> Create Issue on GitHub
                        </Button>
                    </div>
                </div>)}

            {activeTab === "faq" && (<ScrollArea className="h-full">
                     <div className="p-1 pr-4">
                        <Card>
                            <CardHeader>
                                <CardTitle>Frequently Asked Questions</CardTitle>
                            </CardHeader>
                            <CardContent className="space-y-6">
                                {faqs.map((faq, index) => (<div key={index} className="space-y-2">
                                    <h3 className="font-medium text-base text-foreground/90">{faq.q}</h3>
                                    <p className="text-sm text-muted-foreground leading-relaxed">{faq.a}</p>
                                </div>))}
                            </CardContent>
                        </Card>
                    </div>
                </ScrollArea>)}

            {activeTab === "authors" && (<div className="p-1 pr-2">
                    <Card>
                        <CardHeader>
                            <CardTitle>Authors</CardTitle>
                        </CardHeader>
                        <CardContent>
                            <div className="grid gap-3 sm:grid-cols-2">
                                {authors.map(author => (<button key={author.url} type="button" onClick={() => openExternal(author.url)} className="flex items-center justify-between gap-3 rounded-md border bg-background px-4 py-3 text-left transition-colors hover:bg-muted/50 hover:border-primary/50">
                                    <span className="flex min-w-0 items-center gap-3">
                                        <Github className="h-5 w-5 shrink-0"/>
                                        <span className="min-w-0">
                                            <span className="block truncate font-medium">{author.name}</span>
                                            <span className="block truncate text-sm text-muted-foreground">{author.role}</span>
                                        </span>
                                    </span>
                                    <ExternalLink className="h-4 w-4 shrink-0 text-muted-foreground"/>
                                </button>))}
                            </div>
                        </CardContent>
                    </Card>
                </div>)}
        </div>
    </div>);
}
