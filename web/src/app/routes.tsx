import { lazy } from "react";
import { Navigate, type RouteObject } from "react-router";

import { AppLayout } from "@/app/Layout";
import { integrations } from "@/app/nav";
import { HomeScreen } from "@/features/home/HomeScreen";

const TvScreen = lazy(() => import("@/features/tv/TvScreen").then((m) => ({ default: m.TvScreen })));
const GalleryScreen = lazy(() => import("@/features/gallery/GalleryScreen").then((m) => ({ default: m.GalleryScreen })));
const AppleHomeScreen = lazy(() => import("@/features/apple-home/AppleHomeScreen").then((m) => ({ default: m.AppleHomeScreen })));
const RemoteScreen = lazy(() => import("@/features/remote/RemoteScreen").then((m) => ({ default: m.RemoteScreen })));

export const routes: RouteObject[] = [
  {
    path: "/",
    element: <AppLayout />,
    children: [
      { index: true, element: <HomeScreen /> },
      { path: "tv", element: <TvScreen /> },
      { path: "gallery", element: <GalleryScreen /> },
      {
        path: "integrations",
        children: [
          { index: true, element: <Navigate to={integrations[0].to} replace /> },
          { path: "apple-home", element: <AppleHomeScreen /> },
        ],
      },
      { path: "apple-home", element: <Navigate to="/integrations/apple-home" replace /> },
      { path: "remote", element: <RemoteScreen /> },
      { path: "*", element: <Navigate to="/" replace /> },
    ],
  },
];
