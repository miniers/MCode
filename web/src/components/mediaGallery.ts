import type {
  RemoteMediaRef,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult
} from '../types/contract';

/**
 * 大图预览的画廊注册表。
 *
 * MessageMedia 的每个图片缩略图挂载时登记自己、卸载时注销；MediaViewer 打开时按
 * threadID 取出同一会话里的全部图片，按文档顺序定位当前项并支持左右翻页。
 *
 * 排序键用缩略图元素在文档中的位置（compareDocumentPosition）：timeline 按消息顺序
 * 渲染，同一消息内的附件按数组顺序渲染，所以文档顺序就是「消息顺序 + 附件顺序」，
 * 不需要额外的时间戳或 turnIndex。
 *
 * 限制：timeline 是虚拟化列表，尚未渲染的消息里的图片不会出现在注册表中，
 * 因此翻页范围只覆盖当前已渲染的图片；滚动后新挂载的图片才会进入画廊。
 */

export type GalleryReadThreadMedia = (
  params: RemoteThreadsMediaReadParams
) => Promise<RemoteThreadsMediaReadResult>;

export interface GalleryItem {
  /** 唯一标识：threadID:messageID:mediaID（与桌面端用消息 + 媒体 ID 定位一致） */
  key: string;
  threadID: string;
  messageID: string;
  media: RemoteMediaRef;
  /** 缩略图 data URL；原图不可用时兜底，未加载完成前为 null */
  thumbSrc: string | null;
  readThreadMedia: GalleryReadThreadMedia;
  /** 排序锚点：缩略图根元素，用来按文档顺序还原消息与附件顺序 */
  anchor: Element | null;
  /** 登记顺序，文档顺序无法比较（节点已脱离文档）时的兜底 */
  seq: number;
}

export function galleryItemKey(threadID: string, messageID: string, mediaID: string): string {
  return `${threadID}:${messageID}:${mediaID}`;
}

let nextSeq = 0;

/** 已登记的条目：key -> item，同一张图片重复渲染时后者覆盖前者 */
const registry = new Map<string, GalleryItem>();

export function createGalleryItem(input: {
  threadID: string;
  messageID: string;
  media: RemoteMediaRef;
  readThreadMedia: GalleryReadThreadMedia;
}): GalleryItem {
  nextSeq += 1;
  return {
    key: galleryItemKey(input.threadID, input.messageID, input.media.id),
    threadID: input.threadID,
    messageID: input.messageID,
    media: input.media,
    thumbSrc: null,
    readThreadMedia: input.readThreadMedia,
    anchor: null,
    seq: nextSeq
  };
}

/** 登记一张图片，返回注销函数；只有同一个条目对象还在注册表中时才删除，避免误删新挂载的同名条目 */
export function registerGalleryItem(item: GalleryItem): () => void {
  registry.set(item.key, item);
  return () => {
    if (registry.get(item.key) === item) registry.delete(item.key);
  };
}

export function getGalleryItem(key: string): GalleryItem | undefined {
  return registry.get(key);
}

// compareDocumentPosition 的位标记，避免依赖全局 Node
const POSITION_DISCONNECTED = 1;
const POSITION_PRECEDING = 2;
const POSITION_FOLLOWING = 4;

/** 按文档顺序比较两个锚点：a 在前返回 -1，在后返回 1，无法比较返回 0 */
function compareDocumentOrder(a: Element | null, b: Element | null): number {
  if (!a || !b || a === b) return 0;
  if (typeof a.compareDocumentPosition !== 'function') return 0;
  const position = a.compareDocumentPosition(b);
  // 脱离文档的节点只能得到实现相关的顺序，交给 seq 兜底
  if (position & POSITION_DISCONNECTED) return 0;
  if (position & POSITION_FOLLOWING) return -1;
  if (position & POSITION_PRECEDING) return 1;
  return 0;
}

/** 取会话里所有已登记的图片，按消息顺序与附件顺序排好 */
export function getThreadGallery(threadID: string): GalleryItem[] {
  const items: GalleryItem[] = [];
  registry.forEach((item) => {
    if (item.threadID === threadID) items.push(item);
  });
  items.sort((a, b) => compareDocumentOrder(a.anchor, b.anchor) || a.seq - b.seq);
  return items;
}
