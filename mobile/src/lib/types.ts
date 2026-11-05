// The API's own types, generated from the server's OpenAPI description
// (npm run types). Names match app/api/schemas.py on the server.
import type { components } from "./openapi";

type S = components["schemas"];

export type Card = S["Card"];
export type TitleState = S["TitleState"];
export type TitleDetail = S["TitleDetail"];
export type Season = S["Season"];
export type SeasonOut = S["SeasonOut"];
export type Episode = S["Episode"];
export type Watch = S["Watch"];
export type Community = S["Community"];
export type RatingEntry = S["RatingEntry"];
export type Scores = S["Scores"];
export type RowsOut = S["RowsOut"];
export type PosterRow = S["PosterRow"];
export type LandscapeRow = S["LandscapeRow"];
export type UpNextItem = S["UpNextItem"];
export type HomeOut = S["HomeOut"];
export type CardsOut = S["CardsOut"];
export type PageOut = S["PageOut"];
export type ListsOut = S["ListsOut"];
export type ListSummary = S["ListSummary"];
export type ListOut = S["ListOut"];
export type CollectionOut = S["CollectionOut"];
export type SearchOut = S["SearchOut"];
export type AiGuessOut = S["AiGuessOut"];
export type PersonOut = S["PersonOut"];
export type MeOut = S["MeOut"];
export type HistoryOut = S["HistoryOut"];
export type NextEpisodeOut = S["NextEpisodeOut"];
export type PlayInfo = S["PlayInfo"];
export type PlayOut = S["PlayOut"];
export type PlaySessionOut = S["PlaySessionOut"];
export type PlayerSettingsOut = S["PlayerSettingsOut"];
export type JellyfinLink = S["JellyfinLink"];
export type DeviceOut = S["DeviceOut"];
export type EmailSettings = S["EmailSettings"];
export type MDBListExploreOut = S["MDBListExploreOut"];
export type MDBListPreviewOut = S["MDBListPreviewOut"];
export type RelatedOut = S["RelatedOut"];
export type MediaType = "movie" | "tv";
