/** 完整地址（https://…、mailto:…、//host/…）算外链：不走面板内部路由，原样作为 href */
export const isExternalHref = (href: string) => /^([a-z][a-z\d+.-]*:|\/\/)/i.test(href)
