import { defineRailway, github, preserve, project, service, volume } from "railway/iac";

export default defineRailway(() => {
  const chatgptSharePageVolume = volume("chatgpt-share-page-volume", { alerts: { usage: { "100": {}, "80": {}, "95": {} } }, allowOnlineResize: true, region: "asia-southeast1-eqsg3a", sizeMB: 5000 });
  const chatgptSharePage = service("chatgpt-share-page", {
    source: github("jihuayu/chatgpt-share-page", { checkSuites: false }),
    healthcheck: "/healthz",
    healthcheckTimeout: 300,
    replicas: { "asia-southeast1-eqsg3a": 1 },
    volumeMounts: { "/data": chatgptSharePageVolume },
    env: { APP_BASE_URL: preserve(), DATABASE_PATH: preserve(), DATA_DIR: preserve(), PUBLIC_BASE_URL: preserve(), RAILWAY_RUN_UID: preserve() },
  });

  return project("chatgpt-share-page", {
    resources: [chatgptSharePage, chatgptSharePageVolume],
  });
});
