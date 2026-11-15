-- Раунд 31: цена аукционного экземпляра на странице профиля.
--
-- Выигранный в аукционе подарок хранится в peer_star_gifts как обычный экземпляр
-- каталога; до сих пор проекция профиля (payments.getSavedStarGifts /
-- getSavedStarGift) брала цену из ревизии каталога, поэтому карточка подарка
-- показывала фиксированную цену каталога вместо цены выигрышной ставки (та же
-- цена уже показывается в сервисном сообщении победителя — блок StarGift несёт
-- item.amount).
--
-- paid_stars — цена, фактически уплаченная за экземпляр (выигрышная ставка
-- аукциона). 0 — обычное приобретение (покупка/подарок/перенос), цена берётся
-- из ревизии каталога, как раньше. Значение заполняется выдачей аукционных
-- наград (dispatchStarGiftAuctionAwards) вместе с gift_num.
ALTER TABLE public.peer_star_gifts
    ADD COLUMN IF NOT EXISTS paid_stars bigint NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.peer_star_gifts'::regclass
          AND conname = 'peer_star_gifts_paid_stars_check'
    ) THEN
        ALTER TABLE public.peer_star_gifts
            ADD CONSTRAINT peer_star_gifts_paid_stars_check CHECK (paid_stars >= 0);
    END IF;
END;
$$;
